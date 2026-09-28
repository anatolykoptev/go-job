package hunt_test

// account_store_test.go — the P2 account-isolation deny matrix over
// account_job_scores, on real PostgreSQL. Every assertion exercises the
// production facade (*hunt.AccountStore obtained via store.ForAccount) — the
// same type adminui listers and huntworker drive — so a scoping-predicate
// removal fails the exact test that claims the isolation.
//
// RED-on-revert (mutations this matrix must catch):
//   - drop `s.account_id = $N` from the ListShortlist score join → B's read
//     returns A's fit_score → "B must see NULL score" fails.
//   - drop the account predicate from CountScored / UnscoredOpenJobs → the
//     per-account count/pool assertions fail.
//   - write hunt_jobs.fit_* again → the legacy-column readback assertion fails.
//   - reintroduce an unscoped score write → there is no API to express it
//     (compile-loud), and uuid.Nil writes die on the account_id FK.

import (
	"context"
	"testing"
	"time"

	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAccountStore_DenyMatrix(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))
	truncateJobs(t, pool)

	aidA := newScoreAccount(t, pool)
	aidB := newScoreAccount(t, pool)
	a := s.ForAccount(aidA)
	b := s.ForAccount(aidB)
	require.NotEqual(t, aidA, aidB)
	assert.Equal(t, aidA, a.AccountID())
	assert.Equal(t, aidB, b.AccountID())

	// Shared corpus: one open job, owned by no account.
	jobID, outcome, err := s.UpsertJob(ctx, hunt.Job{
		DedupHash: hunt.DedupHash("https://deny.example/jobs/1"),
		Title:     "Deny Matrix Role",
		URL:       "https://deny.example/jobs/1",
		Source:    "deny_test",
		Status:    hunt.StatusOpen,
	})
	require.NoError(t, err)
	require.Equal(t, hunt.OutcomeCreated, outcome)

	// P3: ratings are per-account (hunt_ratings.account_id). BOTH accounts
	// rate the shared job so each sees its own membership row; the score read
	// stays account-isolated below. B's independent rating on the same job is
	// itself a deny-matrix assertion (A's row must not leak).
	require.NoError(t, a.Rate(ctx, "job", jobID, hunt.StageSaved, "", ""))
	require.NoError(t, b.Rate(ctx, "job", jobID, hunt.StageSaved, "", "b note"))

	shortlist := func(as *hunt.AccountStore) []hunt.ShortlistRow {
		t.Helper()
		rows, _, err := as.ListShortlist(ctx, hunt.ShortlistQuery{
			TriageValues: []string{hunt.StageSaved},
			StageValues:  []string{},
		})
		require.NoError(t, err)
		return rows
	}

	// ── 1. A writes; A reads its score; B's view of the same job is NULL ──
	require.NoError(t, a.SetJobScore(ctx, jobID, hunt.ScoreResult{
		FitScore:    91,
		FitBand:     "strong",
		SuccessBand: "STRONG",
		OverUnder:   "well_matched",
		ScoredAt:    time.Now().UTC().Truncate(time.Second),
	}))

	rowsA := shortlist(a)
	require.Len(t, rowsA, 1)
	require.NotNil(t, rowsA[0].FitScore, "A must read A's score")
	assert.Equal(t, 91, *rowsA[0].FitScore)
	assert.Equal(t, "strong", rowsA[0].FitBand)
	assert.Equal(t, "STRONG", rowsA[0].SuccessBand)

	rowsB := shortlist(b)
	require.Len(t, rowsB, 1, "B sees its own rating row — only the score is isolated")
	assert.Nil(t, rowsB[0].FitScore, "B must never read A's score")
	assert.Empty(t, rowsB[0].FitBand)
	assert.Equal(t, "b note", rowsB[0].Note, "B reads its own rating note, never A's")

	// ── 2. CountScored is per-account ──
	assert.Equal(t, 1, a.CountScored(ctx))
	assert.Equal(t, 0, b.CountScored(ctx))

	// ── 3. The unscored pool is per-account ──
	unscA, err := a.UnscoredOpenJobs(ctx, 10, false)
	require.NoError(t, err)
	assert.Empty(t, unscA, "job is scored for A — absent from A's unscored pool")
	unscB, err := b.UnscoredOpenJobs(ctx, 10, false)
	require.NoError(t, err)
	require.Len(t, unscB, 1, "same job is still unscored for B")
	assert.Equal(t, jobID, unscB[0].ID)

	statsA, err := a.UnscoredOpenJobsStats(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, statsA.Count)
	statsB, err := b.UnscoredOpenJobsStats(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, statsB.Count)

	// ── 4. B writes under itself — A's row is untouched ──
	require.NoError(t, b.SetJobScore(ctx, jobID, hunt.ScoreResult{
		FitScore:    12,
		FitBand:     "low",
		SuccessBand: "LONGSHOT",
		OverUnder:   "overqualified",
		ScoredAt:    time.Now().UTC().Truncate(time.Second),
	}))

	rowsA = shortlist(a)
	require.Len(t, rowsA, 1)
	require.NotNil(t, rowsA[0].FitScore)
	assert.Equal(t, 91, *rowsA[0].FitScore, "B's write must not touch A's row")
	rowsB = shortlist(b)
	require.NotNil(t, rowsB[0].FitScore)
	assert.Equal(t, 12, *rowsB[0].FitScore)
	assert.Equal(t, "low", rowsB[0].FitBand)

	assert.Equal(t, 1, a.CountScored(ctx))
	assert.Equal(t, 1, b.CountScored(ctx))

	unscB, err = b.UnscoredOpenJobs(ctx, 10, false)
	require.NoError(t, err)
	assert.Empty(t, unscB, "B's own score drains B's unscored pool")

	// ── 5. The shared corpus row is untouched by either write ──
	var legacyFit *int
	var legacyScoredAt *time.Time
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT fit_score, scored_at FROM hunt_jobs WHERE id = $1`, jobID,
	).Scan(&legacyFit, &legacyScoredAt))
	assert.Nil(t, legacyFit, "hunt_jobs.fit_score is dead to account-scoped writes")
	assert.Nil(t, legacyScoredAt, "hunt_jobs.scored_at is dead to account-scoped writes")

	// ── 6. Exactly two score rows exist, each keyed to its own account ──
	var n int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM account_job_scores WHERE job_id = $1`, jobID).Scan(&n))
	assert.Equal(t, 2, n)
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM account_job_scores WHERE job_id = $1 AND account_id = $2 AND fit_score = 91`,
		jobID, aidA).Scan(&n))
	assert.Equal(t, 1, n, "A's row keyed to A")
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM account_job_scores WHERE job_id = $1 AND account_id = $2 AND fit_score = 12`,
		jobID, aidB).Scan(&n))
	assert.Equal(t, 1, n, "B's row keyed to B")

	// ── 7. Nil account: reads match nothing, writes die on the FK ──
	nilStore := s.ForAccount(uuid.Nil)
	rowsNil := shortlist(nilStore)
	require.Empty(t, rowsNil, "uuid.Nil binds no account — its shortlist is fail-closed empty")
	assert.Equal(t, 0, nilStore.CountScored(ctx))
	err = nilStore.SetJobScore(ctx, jobID, hunt.ScoreResult{ScoredAt: time.Now()})
	require.Error(t, err, "uuid.Nil write must fail at the account_id FK")
	assert.Equal(t, 1, a.CountScored(ctx), "failed nil write must not disturb A")
}

// TestAccountStore_UnscoredPerAccount pins the per-account unscored
// definition: scoring a job for A leaves it in B's sweep pool — each
// account's backlog is its own.
//
// RED-on-revert: probing hunt_jobs.scored_at (or omitting account_id) in
// UnscoredOpenJobs makes the job vanish from B's pool after A scores it.
func TestAccountStore_UnscoredPerAccount(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))
	truncateJobs(t, pool)

	a := s.ForAccount(newScoreAccount(t, pool))
	b := s.ForAccount(newScoreAccount(t, pool))

	jobID, _, err := s.UpsertJob(ctx, hunt.Job{
		DedupHash: hunt.DedupHash("https://deny.example/jobs/unscored"),
		Title:     "Unscored Pool Job",
		URL:       "https://deny.example/jobs/unscored",
		Source:    "deny_test",
		Status:    hunt.StatusOpen,
	})
	require.NoError(t, err)

	require.NoError(t, a.SetJobScore(ctx, jobID, hunt.ScoreResult{
		FitScore: 50, ScoredAt: time.Now(),
	}))

	// rescoreAll=false: B still sees the job as unscored even though A scored it.
	got, err := b.UnscoredOpenJobs(ctx, 10, false)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, jobID, got[0].ID)

	// rescoreAll=true: even A's scored job is re-eligible (one-shot rescore path).
	got, err = a.UnscoredOpenJobs(ctx, 10, true)
	require.NoError(t, err)
	assert.Len(t, got, 1)
}
