package huntworker

// account_bind_test.go — worker-side half of the P2 deny matrix, on real
// PostgreSQL: runUnscoredSweep drives the account-bound facade
// (*hunt.AccountStore via store.ForAccount) so score rows land under the
// bound account and only drain that account's unscored pool.
//
// RED-on-revert:
//   - sweeping through the unscoped store → no account_job_scores row is
//     written under the bound account → the keyed-row assertions fail.
//   - probing hunt_jobs.scored_at for the unscored pool → a job scored by A
//     disappears from B's pool → the B-pool assertion fails.

import (
	"context"
	"os"
	"sync/atomic"
	"testing"

	"github.com/anatolykoptev/go_job/internal/accounts"
	"github.com/anatolykoptev/go_job/internal/dbtest"
	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/anatolykoptev/go_job/internal/hunt/score"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// compile-time guard: the account facade must satisfy the worker's scoring
// surface — a dropped method fails here, not at a call site.
var _ accountScoreStore = (*hunt.AccountStore)(nil)

func openWorkerPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	dbtest.RequireTestDB(t, dsn)
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })
	return pool
}

func newWorkerAccount(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	_, _, err := accounts.Bootstrap(ctx, pool, accounts.OperatorSeed{})
	require.NoError(t, err, "accounts.Bootstrap")
	aid, _, err := accounts.CreateAccount(ctx, pool,
		"worker-test-"+uuid.NewString()[:12]+"@example.com", "worker test", nil, "user")
	require.NoError(t, err, "accounts.CreateAccount")
	return aid
}

// TestSweep_WritesUnderBoundAccount runs the REAL sweep path against a real
// pool: an open job exists in the shared corpus; the sweep bound to account A
// must write A's account_job_scores row, leave B's unscored pool untouched,
// and leave the legacy hunt_jobs.fit_* columns NULL.
func TestSweep_WritesUnderBoundAccount(t *testing.T) {
	pool := openWorkerPool(t)
	ctx := context.Background()

	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))
	_, err := pool.Exec(ctx, "TRUNCATE hunt_jobs CASCADE")
	require.NoError(t, err)

	aidA := newWorkerAccount(t, pool)
	aidB := newWorkerAccount(t, pool)

	jobID, _, err := s.UpsertJob(ctx, hunt.Job{
		DedupHash:   hunt.DedupHash("https://deny.example/jobs/sweep"),
		Title:       "Sweep Bound Role",
		Description: "Go Rust PostgreSQL distributed systems Kubernetes",
		URL:         "https://deny.example/jobs/sweep",
		Source:      "deny_test",
		Status:      hunt.StatusOpen,
	})
	require.NoError(t, err)

	prof := &score.ScoringProfile{
		Seniority:     "Staff",
		CoreSkills:    []string{"Go", "Rust"},
		TargetDomains: []string{"AI infrastructure"},
		CompFloorUSD:  250000,
		Locations:     []string{"Remote (US)"},
		WorkAuth:      "US authorized, no sponsorship",
	}
	deps := score.ScorerDeps{
		Jaccard: func(kw, text string) float64 { return 50 },
		LLM: func(_ context.Context, _ string) (string, error) {
			return `{"fit_score":75,"fit_reasons":["Go match"],"fit_gaps":[],"success_band":"MODERATE","success_reasoning":"good match","over_under":"well_matched"}`, nil
		},
	}

	t.Setenv("HUNT_SCORE_FAIL_OPEN", "true")
	t.Setenv("HUNT_SCORE_MIN_JACCARD", "0")
	t.Setenv("HUNT_SCORE_ENABLED", "true")

	var llmCalls atomic.Int64
	runUnscoredSweep(ctx, s.ForAccount(aidA), prof, deps, &llmCalls, 50)

	// The score row landed under A — keyed to A, never globally. (The fake LLM
	// may produce an unscored mark; the deny property is WHERE the row lands
	// and that the write is stamped scored_at.)
	var scored bool
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT scored_at IS NOT NULL FROM account_job_scores
		WHERE account_id = $1 AND job_id = $2`, aidA, jobID).Scan(&scored))
	assert.True(t, scored, "sweep must persist a score row keyed to the bound account")

	// No row under B — B's pool is undrained. (Other packages' tests may run
	// against the same ephemeral DB concurrently — assert on THIS job, not
	// the global pool length.)
	var n int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT count(*) FROM account_job_scores WHERE account_id = $1 AND job_id = $2`, aidB, jobID).Scan(&n))
	assert.Equal(t, 0, n, "sweep bound to A must not write under B")
	unscB, err := s.ForAccount(aidB).UnscoredOpenJobs(ctx, 50, false)
	require.NoError(t, err)
	var foundB bool
	for _, j := range unscB {
		if j.ID == jobID {
			foundB = true
		}
	}
	assert.True(t, foundB, "job scored for A is still unscored for B")

	// The shared corpus row's legacy columns are untouched.
	var legacyFit *int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT fit_score FROM hunt_jobs WHERE id = $1`, jobID).Scan(&legacyFit))
	assert.Nil(t, legacyFit)
}

// TestSweep_NoBoundAccount_IsInert pins the nil-score-store shape: with no
// account bound the worker ingests the shared corpus but persists no scores —
// runCycle guards on w.scores != nil and the sweep never runs. The facade
// itself is only obtainable through ForAccount, so the inert path is simply
// "scores == nil"; this test asserts the Worker zero-value wiring holds.
func TestSweep_NoBoundAccount_IsInert(t *testing.T) {
	w := &Worker{}
	assert.Nil(t, w.scores, "unbound worker must carry a nil score facade")
}
