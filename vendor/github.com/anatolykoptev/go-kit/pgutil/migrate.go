package pgutil

// migrate.go — version-tracked SQL migration runner.
//
// Semantics:
//   - Advisory lock on a configurable key prevents concurrent boots from racing.
//   - schema_migrations(name, checksum, applied_at) tracks applied files.
//   - Baseline: when tracking table is empty and BaselinePredicate returns
//     true (live DB with existing schema), ALL files are marked applied
//     without executing — safe retrofit for dirty-prod (avoids re-running
//     the migration-008 incident class).
//   - Per-migration transaction: SQL body + INSERT into tracking table commit
//     atomically; failure rolls back so next startup retries cleanly.
//   - Checksum drift on an applied file: Warn + skip (manual intervention).
//   - PreMigrate hook for search_path / extension bootstrap before any SQL.
//
// Constraint: migration files MUST NOT contain explicit transaction control
// (BEGIN/COMMIT) because each file already runs inside a wrapping transaction.
// Statements that require no active transaction, such as CREATE INDEX
// CONCURRENTLY, are NOT supported by this per-file-tx runner.
// TableName MUST be a trusted compile-time constant — it is string-interpolated
// into DDL (SQL identifiers cannot be parameterised).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// defaultMigrateLockKey — fixed int64 namespace for pg_advisory_lock.
// ASCII "GOKIT_MIG" → 0x474F4B49545F4D49. Unlikely to collide with
// app-level advisory locks (which typically use smaller application ints).
const defaultMigrateLockKey int64 = 0x474F4B49545F4D49

// lockDestroyTimeout is the deadline for closing a hijacked connection
// when the advisory lock unlock query fails. Long enough for a TCP teardown
// under transient load, short enough not to stall shutdown.
const lockDestroyTimeout = 5 * time.Second

// validIdentifier matches a plain or schema-qualified SQL identifier.
// Accepts: table_name, schema.table_name — rejects anything with spaces,
// quotes, dashes, semicolons, or other SQL-injection vectors.
var validIdentifier = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*(\.[a-zA-Z_][a-zA-Z0-9_]*)?$`)

// BaselinePredicate is called when the tracking table is empty to decide
// whether the database already has a pre-existing schema. Return true to
// mark all migration files as applied without executing them (safe retrofit
// for live/dirty prod). Return false to run all files in order (fresh DB).
type BaselinePredicate func(ctx context.Context, conn *pgxpool.Conn) (bool, error)

// MigrateOptions configures RunMigrations behavior.
type MigrateOptions struct {
	// TableName is the migration tracking table. Defaults to "schema_migrations".
	// MUST be a trusted constant — it is string-interpolated into DDL.
	// Schema-qualified form allowed: "myschema.schema_migrations".
	TableName string
	// Logger overrides the default slog.Default() logger.
	Logger *slog.Logger
	// Baseline is called when the tracking table is empty. nil → treat empty
	// table as a fresh DB (run all files).
	Baseline BaselinePredicate
	// PreMigrate is called on the migration connection before any migration
	// logic (before reading applied set / calling Baseline). Use for
	// SET search_path, CREATE EXTENSION, AGE bootstrap, etc.
	PreMigrate func(ctx context.Context, conn *pgxpool.Conn) error
	// LockKey overrides the default advisory lock key (0x474F4B49545F4D49).
	// Set a unique value when the default key would collide with another
	// pg_advisory_lock already held by the application on the same database.
	// Zero → use the built-in default.
	LockKey int64
}

func (o *MigrateOptions) applyDefaults() error {
	if o.TableName == "" {
		o.TableName = "schema_migrations"
	}
	if !validIdentifier.MatchString(o.TableName) {
		return fmt.Errorf("pgutil/migrate: TableName %q is not a valid SQL identifier (only [a-zA-Z0-9_.] allowed, schema-qualified dot OK)", o.TableName)
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.LockKey == 0 {
		o.LockKey = defaultMigrateLockKey
	}
	return nil
}

// RunMigrations applies embedded .sql files (sorted by name) tracked in opts.TableName.
//
// Already-applied files are skipped. On an empty tracking table:
//   - If opts.Baseline is non-nil and returns true → mark ALL files applied
//     without executing (safe retrofit for live/dirty prod DBs).
//   - Otherwise → run all files in order (fresh DB path).
//
// Each pending migration runs in its own transaction that also records the
// tracking row — either both commit or both roll back.
//
// Constraint: migration files MUST NOT contain explicit transaction control
// (BEGIN/COMMIT — they would break the wrapping per-file transaction).
// Statements requiring no active transaction (e.g. CREATE INDEX CONCURRENTLY)
// are NOT supported by this runner. Use a separate migration path for those.
//
// fsys is typically an embed.FS passed by the consumer:
//
//	//go:embed migrations/*.sql
//	var migrationsFS embed.FS
//	pgutil.RunMigrations(ctx, pool, migrationsFS, pgutil.MigrateOptions{...})
func RunMigrations(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS, opts MigrateOptions) error {
	if err := opts.applyDefaults(); err != nil {
		return err
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("pgutil/migrate: acquire connection: %w", err)
	}
	defer conn.Release()

	if err := acquireLock(ctx, conn, opts.LockKey); err != nil {
		return err
	}
	defer releaseLock(ctx, conn, opts.Logger, opts.LockKey)

	return runMigrationsOnConn(ctx, conn, fsys, opts)
}

// acquireLock acquires the shared advisory migration lock.
func acquireLock(ctx context.Context, conn *pgxpool.Conn, lockKey int64) error {
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, lockKey); err != nil {
		return fmt.Errorf("pgutil/migrate: acquire advisory lock: %w", err)
	}
	return nil
}

// releaseLock releases the advisory lock. If the unlock query fails (transient
// blip unrelated to ctx cancellation) the connection is force-destroyed via
// Hijack so the pool never returns it with the SESSION-scoped lock still held.
// The ctx-cancel path is already safe: a cancelled exec leaves the conn busy,
// and pgxpool destroys busy conns automatically on Release.
func releaseLock(ctx context.Context, conn *pgxpool.Conn, log *slog.Logger, lockKey int64) {
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, lockKey); err != nil {
		log.WarnContext(ctx, "pgutil/migrate: release advisory lock failed — destroying connection to prevent lock leak",
			slog.Any("error", err))
		// Hijack removes the conn from the pool (Release becomes a no-op).
		// Closing the hijacked *pgx.Conn terminates the backend, which causes
		// Postgres to release all SESSION-scoped advisory locks it held.
		raw := conn.Hijack()
		closeCtx, cancel := context.WithTimeout(context.Background(), lockDestroyTimeout)
		defer cancel()
		_ = raw.Close(closeCtx)
	}
}

// runMigrationsOnConn holds all migration logic after the lock is acquired.
func runMigrationsOnConn(ctx context.Context, conn *pgxpool.Conn, fsys fs.FS, opts MigrateOptions) error {
	log := opts.Logger

	// Optional pre-migration hook (search_path, extensions, graph bootstrap…).
	if opts.PreMigrate != nil {
		if err := opts.PreMigrate(ctx, conn); err != nil {
			return fmt.Errorf("pgutil/migrate: PreMigrate: %w", err)
		}
	}

	// Ensure tracking table exists.
	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS `+opts.TableName+` (
			name       TEXT        PRIMARY KEY,
			checksum   TEXT        NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`); err != nil {
		return fmt.Errorf("pgutil/migrate: create tracking table: %w", err)
	}

	files, err := listMigrationFiles(fsys)
	if err != nil {
		return fmt.Errorf("pgutil/migrate: list files: %w", err)
	}

	baselined, err := tryBaseline(ctx, conn, log, fsys, files, opts)
	if err != nil {
		return err
	}
	if baselined {
		return nil
	}

	return applyPending(ctx, conn, log, fsys, files, opts.TableName)
}

// tryBaseline checks whether the tracking table is empty and, if so, whether
// the Baseline predicate indicates a live schema. Returns true when all files
// were marked applied without executing (baseline path taken).
func tryBaseline(
	ctx context.Context, conn *pgxpool.Conn, log *slog.Logger,
	fsys fs.FS, files []string, opts MigrateOptions,
) (bool, error) {
	if opts.Baseline == nil {
		return false, nil
	}
	var count int
	if err := conn.QueryRow(ctx, `SELECT COUNT(*) FROM `+opts.TableName).Scan(&count); err != nil {
		return false, fmt.Errorf("pgutil/migrate: count tracking rows: %w", err)
	}
	if count > 0 {
		return false, nil
	}
	live, err := opts.Baseline(ctx, conn)
	if err != nil {
		return false, fmt.Errorf("pgutil/migrate: baseline predicate: %w", err)
	}
	if !live {
		return false, nil
	}
	return true, baselineAllFiles(ctx, conn, log, fsys, files, opts.TableName)
}

// applyPending loads the applied set and applies any new files in sorted order.
func applyPending(
	ctx context.Context, conn *pgxpool.Conn, log *slog.Logger,
	fsys fs.FS, files []string, table string,
) error {
	applied, err := loadApplied(ctx, conn, table)
	if err != nil {
		return fmt.Errorf("pgutil/migrate: load applied: %w", err)
	}
	for _, name := range files {
		if err := applyFile(ctx, conn, log, fsys, name, applied, table); err != nil {
			return err
		}
	}
	return nil
}

// applyFile applies a single migration file if not already applied.
// Warns on checksum drift without re-running.
func applyFile(
	ctx context.Context, conn *pgxpool.Conn, log *slog.Logger,
	fsys fs.FS, name string, applied map[string]string, table string,
) error {
	content, err := readFile(fsys, name)
	if err != nil {
		return fmt.Errorf("pgutil/migrate: read %s: %w", name, err)
	}
	sum := sha256hex(content)

	if prevSum, ok := applied[name]; ok {
		if prevSum != sum {
			log.WarnContext(ctx, "pgutil/migrate: checksum drift — file changed after apply",
				slog.String("name", name),
				slog.String("applied_sha", prevSum[:12]),
				slog.String("current_sha", sum[:12]),
				slog.String("action", "no re-apply; manual intervention required"))
		}
		return nil
	}

	log.InfoContext(ctx, "pgutil/migrate: applying", slog.String("name", name), slog.String("sha", sum[:12]))
	if err := applyOne(ctx, conn, name, string(content), sum, table); err != nil {
		return fmt.Errorf("pgutil/migrate: apply %s: %w", name, err)
	}
	log.InfoContext(ctx, "pgutil/migrate: applied", slog.String("name", name))
	return nil
}

// baselineAllFiles inserts every file into the tracking table without running
// the SQL. Safe for dirty-prod retrofit: existing schema stays intact.
//
// ON CONFLICT DO NOTHING is intentional here: baseline is idempotent-by-design
// (the count==0 guard above prevents double-baseline in the normal path, but
// concurrent boots with empty tables could race to this point — the winner's
// rows land, the loser's inserts are silently ignored rather than failing).
// This is asymmetric vs applyOne, which uses a bare INSERT to get a hard PK
// failure if the same migration somehow gets tracked twice (a bug, not a race).
func baselineAllFiles(
	ctx context.Context, conn *pgxpool.Conn, log *slog.Logger,
	fsys fs.FS, files []string, table string,
) error {
	for _, name := range files {
		content, err := readFile(fsys, name)
		if err != nil {
			return fmt.Errorf("pgutil/migrate: baseline read %s: %w", name, err)
		}
		sum := sha256hex(content)
		if _, err := conn.Exec(ctx,
			`INSERT INTO `+table+`(name, checksum) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			name, sum,
		); err != nil {
			return fmt.Errorf("pgutil/migrate: baseline insert %s: %w", name, err)
		}
	}
	log.InfoContext(ctx, "pgutil/migrate: baselined existing schema — all files marked applied, none executed",
		slog.Int("count", len(files)))
	return nil
}

func applyOne(ctx context.Context, conn *pgxpool.Conn, name, content, sum, table string) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, content); err != nil {
		return fmt.Errorf("exec body: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO `+table+`(name, checksum) VALUES ($1, $2)`,
		name, sum,
	); err != nil {
		return fmt.Errorf("insert tracking row: %w", err)
	}
	return tx.Commit(ctx)
}

func loadApplied(ctx context.Context, conn *pgxpool.Conn, table string) (map[string]string, error) {
	rows, err := conn.Query(ctx, `SELECT name, checksum FROM `+table+` ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := make(map[string]string)
	for rows.Next() {
		var name, sum string
		if err := rows.Scan(&name, &sum); err != nil {
			return nil, err
		}
		m[name] = sum
	}
	return m, rows.Err()
}

func listMigrationFiles(fsys fs.FS) ([]string, error) {
	var out []string
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".sql") {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

func readFile(fsys fs.FS, name string) ([]byte, error) {
	return fs.ReadFile(fsys, name)
}

func sha256hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
