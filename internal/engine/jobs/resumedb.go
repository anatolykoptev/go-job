package jobs

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema/*.sql
var schemaFS embed.FS

// ageSetup runs per-connection AGE initialization.
const ageSetup = `LOAD 'age'; SET search_path TO ag_catalog, "$user", public`

// Package-level singletons, set from main.go.
var (
	resumeDB *ResumeDB
	memDB    *MemDBClient
)

// SetResumeDB sets the package-level resume DB instance.
func SetResumeDB(db *ResumeDB) { resumeDB = db }

// GetResumeDB returns the package-level resume DB instance (may be nil).
func GetResumeDB() *ResumeDB { return resumeDB }

// SetMemDB sets the package-level MemDB client instance.
func SetMemDB(c *MemDBClient) { memDB = c }

// GetMemDB returns the package-level MemDB client instance (may be nil).
func GetMemDB() *MemDBClient { return memDB }

// ResumeDB holds the pgx connection pool for resume storage.
type ResumeDB struct {
	pool *pgxpool.Pool
}

// ConnectResumeDB creates a pgx pool and runs schema migrations.
func ConnectResumeDB(ctx context.Context, databaseURL string) (*ResumeDB, error) {
	if databaseURL == "" {
		return nil, errors.New("DATABASE_URL is required")
	}

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	config.MaxConns = 10
	config.MinConns = 1

	// Force search_path to public for all pool connections.
	// The memos role has ag_catalog first in search_path (for MemDB graph),
	// which causes CREATE TABLE / INSERT to resolve to ag_catalog instead of public.
	// AGE graph queries explicitly set their own search_path via ageSetup.
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET search_path TO public")
		return err
	}

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create pgx pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	db := &ResumeDB{pool: pool}
	if err := db.runMigrations(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	slog.Info("resume postgres connected", slog.String("addr", config.ConnConfig.Host))
	return db, nil
}

func (db *ResumeDB) Close() {
	db.pool.Close()
}

func (db *ResumeDB) runMigrations(ctx context.Context) error {
	entries, err := schemaFS.ReadDir("schema")
	if err != nil {
		return fmt.Errorf("read schema dir: %w", err)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	// Run migrations on a single dedicated connection to avoid search_path issues
	// across pooled connections. The memos role has ag_catalog first in search_path.
	conn, err := db.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Release()

	// Ensure we create tables in public schema
	if _, err := conn.Exec(ctx, "SET search_path TO public"); err != nil {
		return fmt.Errorf("set search_path: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		data, err := schemaFS.ReadFile("schema/" + entry.Name())
		if err != nil {
			return fmt.Errorf("read %s: %w", entry.Name(), err)
		}

		if _, err := conn.Exec(ctx, string(data)); err != nil {
			if strings.Contains(entry.Name(), "002") {
				slog.Warn("AGE migration failed (Apache AGE may not be installed)",
					slog.String("file", entry.Name()),
					slog.Any("error", err))
				// Reset search_path after AGE migration (it sets search_path to ag_catalog)
				_, _ = conn.Exec(ctx, "SET search_path TO public")
				continue
			}
			return fmt.Errorf("execute %s: %w", entry.Name(), err)
		}

		// 002_resume_graph.sql sets search_path to ag_catalog; reset it for subsequent migrations
		if strings.Contains(entry.Name(), "002") {
			_, _ = conn.Exec(ctx, "SET search_path TO public")
		}

		slog.Info("migration applied", slog.String("file", entry.Name()))
	}
	return nil
}

// --- Person CRUD ---

type PersonRecord struct {
	ID       int               `json:"id"`
	Name     string            `json:"name"`
	Email    string            `json:"email"`
	Phone    string            `json:"phone"`
	Location string            `json:"location"`
	Links    map[string]string `json:"links"`
	Summary  string            `json:"summary"`
}

func (db *ResumeDB) InsertPerson(ctx context.Context, p PersonRecord) (int, error) {
	linksJSON, _ := json.Marshal(p.Links)
	var id int
	err := db.pool.QueryRow(ctx,
		`INSERT INTO resume_persons (name, email, phone, location, links, summary)
		 VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		p.Name, p.Email, p.Phone, p.Location, linksJSON, p.Summary,
	).Scan(&id)
	return id, err
}

func (db *ResumeDB) ClearPerson(ctx context.Context, personID int) error {
	_, err := db.pool.Exec(ctx, `DELETE FROM resume_persons WHERE id = $1`, personID)
	return err
}

// ClearAllPersons deletes all resume data (single-user system, rebuild from scratch).
func (db *ResumeDB) ClearAllPersons(ctx context.Context) error {
	_, err := db.pool.Exec(ctx, `DELETE FROM resume_persons`)
	return err
}

// GetLatestPersonID returns the ID of the most recently created person, or 0 if none.
func (db *ResumeDB) GetLatestPersonID(ctx context.Context) int {
	var id int
	err := db.pool.QueryRow(ctx, `SELECT id FROM resume_persons ORDER BY id DESC LIMIT 1`).Scan(&id)
	if err != nil {
		return 0
	}
	return id
}

// GetPerson returns the person record for the given ID.
func (db *ResumeDB) GetPerson(ctx context.Context, personID int) (*PersonRecord, error) {
	var p PersonRecord
	var linksJSON []byte
	err := db.pool.QueryRow(ctx,
		`SELECT id, name, COALESCE(email,''), COALESCE(phone,''), COALESCE(location,''), COALESCE(links,'{}'), COALESCE(summary,'')
		 FROM resume_persons WHERE id = $1`, personID,
	).Scan(&p.ID, &p.Name, &p.Email, &p.Phone, &p.Location, &linksJSON, &p.Summary)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(linksJSON, &p.Links)
	return &p, nil
}

// GetPersonEnrichedAt returns the enriched_at timestamp as a string, or empty if not enriched.
func (db *ResumeDB) GetPersonEnrichedAt(ctx context.Context, personID int) string {
	var enrichedAt *string
	err := db.pool.QueryRow(ctx,
		`SELECT enriched_at::text FROM resume_persons WHERE id = $1`, personID,
	).Scan(&enrichedAt)
	if err != nil || enrichedAt == nil {
		return ""
	}
	return *enrichedAt
}
