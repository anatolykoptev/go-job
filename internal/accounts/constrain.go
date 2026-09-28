package accounts

// constrain.go — the P5 constrain half of the ADR-13 expand/backfill/constrain
// migration. The expand (ADD COLUMN) and backfill (stamp-to-operator / purge)
// halves land earlier in Bootstrap via the Ensure*AccountScope steps and the
// Backfill* functions; ConstrainAccountColumns runs LAST — after the operator
// seed and every backfill — and is the DATA GATE for the whole cutover:
//
//   - For every account-bearing table that still ships a nullable account_id
//     (hunt_ratings, resume_persons, resume_vectors, oversize_responses) it
//     first counts `account_id IS NULL` rows. ANY positive count fails the
//     boot loudly, naming each table with its count — the constrain REFUSES
//     on dirty data instead of dying mid-migration on a constraint violation.
//     The operator fixes ownership (or deletes the rows) and reboots.
//   - On clean data it issues ALTER COLUMN … SET NOT NULL, re-asserts the
//     account_id FKs as UNCONDITIONAL guarded blocks (Bootstrap can assume
//     panel_accounts — it created it; the to_regclass guards in the bare-DB
//     schema sweep files stay for their standalone-apply path, issue #494),
//     and re-verifies the UNIQUE scope swaps (hunt_ratings was swapped by
//     hunt schema 014, resume_vectors by EnsureResumeAccountScope — the
//     re-issues here make this step self-contained and convergence-proof).
//
// There is NO runtime flag gating multi-account reads (checked: no
// MULTI_ACCOUNT env exists — the account scope is structural via ForAccount
// facades). This constrain step IS the cutover flip, and it is data-gated by
// design: schema refuses to declare the constrained state while the data
// cannot satisfy it. The irreversible half (dropping user_name / legacy
// hunt_jobs.fit_* / the hunt_settings id=1 row) stays in the post-soak PR
// per ADR-13.
//
// Ordering note (fresh DBs): Bootstrap precedes hStore.Migrate and
// oversize.Migrate, so on a brand-new database the late-bound tables do not
// exist yet when this runs — the to_regclass probes skip them, the migration
// files create the column nullable, and the NEXT boot constrains. Fresh DBs
// converge one boot late; existing DBs constrain on the deploy that ships
// this code. Both are safe: a nullable account_id on an empty table accepts
// only the always-scoped facade writes.

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// accountConstrainSpec describes one expand-half account-bearing table the
// constrain step owns: the table name, its account_id FK constraint name
// (re-asserted unconditionally — Bootstrap guarantees panel_accounts), the
// legacy (…, user_name) UNIQUE to drop if it still coexists, and the
// account-scoped UNIQUE that must be present in the final state.
type accountConstrainSpec struct {
	table         string
	fkName        string
	legacyUnique  string // "" = none
	scopedUnique  string // "" = none
	scopedColumns string // e.g. "(entry_kind, entry_id, account_id)"
}

// constrainTables is the full P5 constraint set — every table that grew a
// NULLABLE account_id during the expand phases. account_job_scores,
// mcp_api_keys and account_hunt_settings are born NOT NULL in their
// Bootstrap-owned DDL and are deliberately absent here.
var constrainTables = []accountConstrainSpec{
	{
		table:         "hunt_ratings",
		fkName:        "hunt_ratings_account_id_fkey",
		legacyUnique:  "hunt_ratings_entry_kind_entry_id_user_name_key",
		scopedUnique:  "hunt_ratings_entry_account_key",
		scopedColumns: "(entry_kind, entry_id, account_id)",
	},
	{
		table:  "resume_persons",
		fkName: "resume_persons_account_id_fkey",
	},
	{
		table:         "resume_vectors",
		fkName:        "resume_vectors_account_id_fkey",
		legacyUnique:  "resume_vectors_user_name_content_hash_key",
		scopedUnique:  "resume_vectors_account_content_key",
		scopedColumns: "(account_id, content_hash)",
	},
	{
		table:  "oversize_responses",
		fkName: "oversize_responses_account_id_fkey",
	},
}

// ConstrainAccountColumns is the P5 data-gated constrain step (plan ADR-13).
// It runs at the END of Bootstrap — after EnsureSchema, the scope ensures,
// the operator seed and every backfill — so the only reason a NULL
// account_id can still exist is data no backfill owns. That state fails the
// boot with a per-table count; clean state applies SET NOT NULL + re-asserts
// the FKs and UNIQUE scope swaps in a single implicit-transaction batch.
//
// Idempotent: a second run finds every column already NOT NULL, issues only
// the guarded re-asserts (all existence-probed) and verifies the final
// state. A failure mid-batch rolls the whole batch back (simple-protocol
// multi-statement Exec is one implicit transaction), so a partial constrain
// can never persist.
func ConstrainAccountColumns(ctx context.Context, pool *pgxpool.Pool) error {
	// Phase 1 — survey: which tables exist, which still carry a nullable
	// account_id, and how dirty each nullable column is. Absent tables are
	// skipped (fresh DB — their schema migrates AFTER Bootstrap; the next
	// boot constrains them).
	var batch strings.Builder
	var postCheck []accountConstrainSpec
	var dirty []string
	for _, spec := range constrainTables {
		var reg *string
		if err := pool.QueryRow(ctx,
			"SELECT to_regclass($1)", "public."+spec.table).Scan(&reg); err != nil {
			return fmt.Errorf("constrain %s: probe table: %w", spec.table, err)
		}
		if reg == nil {
			continue // fresh DB: migration runner creates the scope later
		}
		var nullable string
		err := pool.QueryRow(ctx, `
			SELECT is_nullable FROM information_schema.columns
			WHERE table_schema = current_schema()
			  AND table_name = $1 AND column_name = 'account_id'`,
			spec.table).Scan(&nullable)
		if err != nil {
			return fmt.Errorf("constrain %s: probe account_id column: %w", spec.table, err)
		}
		postCheck = append(postCheck, spec)
		if nullable != "YES" {
			continue // already constrained — re-assert guards below
		}
		var nulls int64
		if err := pool.QueryRow(ctx, fmt.Sprintf(
			"SELECT count(*) FROM %s WHERE account_id IS NULL", spec.table),
		).Scan(&nulls); err != nil {
			return fmt.Errorf("constrain %s: count NULL account_id: %w", spec.table, err)
		}
		if nulls > 0 {
			dirty = append(dirty, fmt.Sprintf("%s=%d", spec.table, nulls))
			continue
		}
		fmt.Fprintf(&batch,
			"ALTER TABLE %s ALTER COLUMN account_id SET NOT NULL;\n", spec.table)
	}
	if len(dirty) > 0 {
		return fmt.Errorf(
			"constrain refused: NULL account_id rows remain [%s] — "+
				"backfill them to a real panel_accounts id or delete them, then reboot "+
				"(data gate, plan ADR-13; the columns stay nullable until clean)",
			strings.Join(dirty, ", "))
	}
	if len(postCheck) == 0 {
		return nil // nothing migrated yet
	}

	// Phase 2 — apply. FKs are re-asserted UNCONDITIONALLY (existence-guarded
	// only): Bootstrap created panel_accounts, so the to_regclass guards the
	// bare-DB sweep files need are unnecessary here (issue #494 — Bootstrap
	// is where the conditional gap closes). Legacy user_name-scoped UNIQUEs
	// drop and the account-scoped ones re-assert, so a DB that somehow
	// skipped an Ensure step still converges.
	for _, spec := range postCheck {
		fmt.Fprintf(&batch, `
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = '%[1]s'
          AND conrelid = '%[2]s'::regclass
          AND contype = 'f'
    ) THEN
        ALTER TABLE %[2]s
            ADD CONSTRAINT %[1]s
            FOREIGN KEY (account_id) REFERENCES panel_accounts(id);
    END IF;
END $$;
`, spec.fkName, spec.table)
		if spec.legacyUnique != "" {
			fmt.Fprintf(&batch,
				"ALTER TABLE %s DROP CONSTRAINT IF EXISTS %s;\n",
				spec.table, spec.legacyUnique)
		}
		if spec.scopedUnique != "" {
			fmt.Fprintf(&batch, `
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = '%[1]s'
          AND conrelid = '%[2]s'::regclass
          AND contype = 'u'
    ) THEN
        ALTER TABLE %[2]s
            ADD CONSTRAINT %[1]s UNIQUE %[3]s;
    END IF;
END $$;
`, spec.scopedUnique, spec.table, spec.scopedColumns)
		}
	}
	if sql := batch.String(); strings.TrimSpace(sql) != "" {
		if _, err := pool.Exec(ctx, sql); err != nil {
			return fmt.Errorf("constrain apply: %w", err)
		}
	}

	// Phase 3 — verify: every present table must now report is_nullable=NO
	// and carry its FK. A drifted constraint name or a failed ALTER is loud
	// here rather than discovered by the deny matrix.
	for _, spec := range postCheck {
		var nullable string
		if err := pool.QueryRow(ctx, `
			SELECT is_nullable FROM information_schema.columns
			WHERE table_schema = current_schema()
			  AND table_name = $1 AND column_name = 'account_id'`,
			spec.table).Scan(&nullable); err != nil {
			return fmt.Errorf("constrain %s: verify column: %w", spec.table, err)
		}
		if nullable != "NO" {
			return fmt.Errorf("constrain %s: account_id still nullable after apply", spec.table)
		}
		var fkOK bool
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM pg_constraint
				WHERE conname = $1 AND conrelid = $2::regclass AND contype = 'f'
			)`, spec.fkName, spec.table).Scan(&fkOK); err != nil {
			return fmt.Errorf("constrain %s: verify FK: %w", spec.table, err)
		}
		if !fkOK {
			return fmt.Errorf("constrain %s: FK %s missing after apply", spec.table, spec.fkName)
		}
		if spec.scopedUnique != "" {
			var uqOK bool
			if err := pool.QueryRow(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM pg_constraint
					WHERE conname = $1 AND conrelid = $2::regclass AND contype = 'u'
				)`, spec.scopedUnique, spec.table).Scan(&uqOK); err != nil {
				return fmt.Errorf("constrain %s: verify UNIQUE: %w", spec.table, err)
			}
			if !uqOK {
				return fmt.Errorf("constrain %s: UNIQUE %s missing after apply", spec.table, spec.scopedUnique)
			}
		}
	}
	return nil
}
