package hunt_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// openTestPool returns a pool for integration tests.
// Skips if DATABASE_URL is unset.
func openTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping store integration tests")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err, "open test pool")

	t.Cleanup(func() { pool.Close() })
	return pool
}

func TestStore_MigrateIdempotent(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()

	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx), "first migrate")
	require.NoError(t, s.Migrate(ctx), "second migrate must be idempotent")
}

func truncateBounties(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), "TRUNCATE hunt_bounties CASCADE")
	require.NoError(t, err)
}

func TestStore_UpsertBounty_Created(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))
	truncateBounties(t, pool)

	b := hunt.Bounty{
		DedupHash:   hunt.DedupHash("https://github.com/org/repo/issues/1"),
		Title:       "Fix the bug",
		URL:         "https://github.com/org/repo/issues/1",
		Org:         "org",
		Source:      "algora",
		AmountCents: 50000,
		Currency:    "USD",
		Skills:      []string{"go", "rust"},
	}

	id, outcome, err := s.UpsertBounty(ctx, b)
	require.NoError(t, err)
	assert.Greater(t, id, int64(0))
	assert.Equal(t, hunt.OutcomeCreated, outcome)
}

func TestStore_UpsertBounty_Merged(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))
	truncateBounties(t, pool)

	b := hunt.Bounty{
		DedupHash: hunt.DedupHash("https://github.com/org/repo/issues/2"),
		Title:     "Another bug",
		URL:       "https://github.com/org/repo/issues/2",
		Source:    "algora",
	}

	id1, outcome1, err := s.UpsertBounty(ctx, b)
	require.NoError(t, err)
	assert.Equal(t, hunt.OutcomeCreated, outcome1)

	// Wait a tick so timestamps differ.
	time.Sleep(5 * time.Millisecond)

	id2, outcome2, err := s.UpsertBounty(ctx, b)
	require.NoError(t, err)
	assert.Equal(t, hunt.OutcomeMerged, outcome2)
	assert.Equal(t, id1, id2, "Merged upsert must return the existing row id")
}

func TestStore_UpsertBounty_MergedUpdatesLastSeen(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))
	truncateBounties(t, pool)

	b := hunt.Bounty{
		DedupHash: hunt.DedupHash("https://github.com/org/repo/issues/3"),
		Title:     "Dedup timing",
		URL:       "https://github.com/org/repo/issues/3",
		Source:    "algora",
	}

	id, _, err := s.UpsertBounty(ctx, b)
	require.NoError(t, err)

	got1, err := s.GetBounty(ctx, id)
	require.NoError(t, err)
	firstSeen := got1.LastSeenAt

	time.Sleep(10 * time.Millisecond)
	_, _, err = s.UpsertBounty(ctx, b)
	require.NoError(t, err)

	got2, err := s.GetBounty(ctx, id)
	require.NoError(t, err)

	assert.True(t, got2.LastSeenAt.After(firstSeen),
		"last_seen_at must advance on merge: first=%v second=%v", firstSeen, got2.LastSeenAt)
}

func TestStore_ListBounties_FilterBySource(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))
	truncateBounties(t, pool)

	for i, src := range []string{"algora", "algora", "opire"} {
		_, _, err := s.UpsertBounty(ctx, hunt.Bounty{
			DedupHash: hunt.DedupHash("https://example.com/" + src + "/" + string(rune('a'+i))),
			Title:     "Bounty " + string(rune('a'+i)),
			URL:       "https://example.com/" + src + "/" + string(rune('a'+i)),
			Source:    src,
		})
		require.NoError(t, err)
	}

	algora, err := s.ListBounties(ctx, hunt.BountyFilter{Source: "algora", Limit: 10})
	require.NoError(t, err)
	assert.Len(t, algora, 2, "should return only algora bounties")

	opire, err := s.ListBounties(ctx, hunt.BountyFilter{Source: "opire", Limit: 10})
	require.NoError(t, err)
	assert.Len(t, opire, 1)
}

func TestStore_GetBounty_NotFound(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))

	_, err := s.GetBounty(ctx, 999999999)
	assert.ErrorIs(t, err, hunt.ErrNotFound)
}

// --- Jobs ---

func truncateJobs(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), "TRUNCATE hunt_jobs CASCADE")
	require.NoError(t, err)
}

func TestStore_UpsertJob_Created(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))
	truncateJobs(t, pool)

	j := hunt.Job{
		DedupHash: hunt.DedupHash("https://company.com/jobs/123"),
		Title:     "Senior Go Engineer",
		Company:   "ACME Corp",
		URL:       "https://company.com/jobs/123",
		Source:    "linkedin",
		Skills:    []string{"go"},
	}

	id, outcome, err := s.UpsertJob(ctx, j)
	require.NoError(t, err)
	assert.Greater(t, id, int64(0))
	assert.Equal(t, hunt.OutcomeCreated, outcome)
}

func TestStore_UpsertJob_Merged(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))
	truncateJobs(t, pool)

	j := hunt.Job{
		DedupHash: hunt.DedupHash("https://company.com/jobs/456"),
		Title:     "Go Dev",
		URL:       "https://company.com/jobs/456",
		Source:    "indeed",
	}

	_, outcome1, err := s.UpsertJob(ctx, j)
	require.NoError(t, err)
	assert.Equal(t, hunt.OutcomeCreated, outcome1)

	_, outcome2, err := s.UpsertJob(ctx, j)
	require.NoError(t, err)
	assert.Equal(t, hunt.OutcomeMerged, outcome2)
}

func TestStore_GetJob_NotFound(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))

	_, err := s.GetJob(ctx, 999999999)
	assert.ErrorIs(t, err, hunt.ErrNotFound)
}

// --- Freelance ---

func truncateFreelance(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), "TRUNCATE hunt_freelance CASCADE")
	require.NoError(t, err)
}

func TestStore_UpsertFreelance_Created(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))
	truncateFreelance(t, pool)

	f := hunt.Freelance{
		DedupHash: hunt.DedupHash("https://upwork.com/jobs/golang-dev"),
		Title:     "Golang API Developer",
		URL:       "https://upwork.com/jobs/golang-dev",
		Platform:  "upwork",
		Source:    "freelancer_api",
		Skills:    []string{"go", "grpc"},
	}

	id, outcome, err := s.UpsertFreelance(ctx, f)
	require.NoError(t, err)
	assert.Greater(t, id, int64(0))
	assert.Equal(t, hunt.OutcomeCreated, outcome)
}

func TestStore_UpsertFreelance_Merged(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))
	truncateFreelance(t, pool)

	f := hunt.Freelance{
		DedupHash: hunt.DedupHash("https://upwork.com/jobs/another"),
		Title:     "Another Project",
		URL:       "https://upwork.com/jobs/another",
		Platform:  "upwork",
		Source:    "freelancer_api",
	}

	_, outcome1, err := s.UpsertFreelance(ctx, f)
	require.NoError(t, err)
	assert.Equal(t, hunt.OutcomeCreated, outcome1)

	_, outcome2, err := s.UpsertFreelance(ctx, f)
	require.NoError(t, err)
	assert.Equal(t, hunt.OutcomeMerged, outcome2)
}

// --- Security ---

func truncateSecurity(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), "TRUNCATE hunt_security CASCADE")
	require.NoError(t, err)
}

func TestStore_UpsertSecurity_Created(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))
	truncateSecurity(t, pool)

	sec := hunt.Security{
		DedupHash:   hunt.DedupHash("https://hackerone.com/programs/target"),
		Name:        "Target Bug Bounty",
		URL:         "https://hackerone.com/programs/target",
		Platform:    "hackerone",
		ProgramType: "bug_bounty",
		MaxBounty:   50000,
	}

	id, outcome, err := s.UpsertSecurity(ctx, sec)
	require.NoError(t, err)
	assert.Greater(t, id, int64(0))
	assert.Equal(t, hunt.OutcomeCreated, outcome)
}

func TestStore_UpsertSecurity_Merged(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))
	truncateSecurity(t, pool)

	sec := hunt.Security{
		DedupHash: hunt.DedupHash("https://hackerone.com/programs/other"),
		Name:      "Other Program",
		URL:       "https://hackerone.com/programs/other",
		Platform:  "hackerone",
	}

	_, outcome1, err := s.UpsertSecurity(ctx, sec)
	require.NoError(t, err)
	assert.Equal(t, hunt.OutcomeCreated, outcome1)

	_, outcome2, err := s.UpsertSecurity(ctx, sec)
	require.NoError(t, err)
	assert.Equal(t, hunt.OutcomeMerged, outcome2)
}

// TestStore_UpsertJob_WeakThenOk verifies that a weak ingest followed by an ok
// re-ingest of the same URL promotes the stored title/description/company/skills
// (fill-only CASE WHEN logic in UpsertJob ON CONFLICT). The test goes RED if
// the CASE WHEN guards are removed and DO UPDATE reverts to overwriting blindly.
func TestStore_UpsertJob_WeakThenOk(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))
	truncateJobs(t, pool)

	url := "https://jobs.example.com/sre-vacancy"
	hash := hunt.DedupHash(url)

	// Step 1: weak ingest — LLM returned nothing; raw HTML blob in description, title empty.
	weak := hunt.Job{
		DedupHash:   hash,
		Title:       "", // empty — the hallmark of a weak ingest
		Company:     "",
		URL:         url,
		Source:      "vacancy_ingest",
		Description: "<html><body>raw html blob</body></html>",
	}
	_, outcome1, err := s.UpsertJob(ctx, weak)
	require.NoError(t, err)
	assert.Equal(t, hunt.OutcomeCreated, outcome1, "weak ingest should create the row")

	// Step 2: ok re-ingest — same URL, now LLM extracted good data.
	ok := hunt.Job{
		DedupHash:   hash,
		Title:       "Senior SRE",
		Company:     "ACME",
		URL:         url,
		Source:      "vacancy_ingest",
		Description: "Manages oncall rotation and reliability improvements.",
		Skills:      []string{"go", "k8s"},
	}
	_, outcome2, err := s.UpsertJob(ctx, ok)
	require.NoError(t, err)
	assert.Equal(t, hunt.OutcomeMerged, outcome2, "re-ingest of same URL should merge")

	// Verify the row now carries the good data from the ok ingest.
	jobs, err := s.ListJobs(ctx, hunt.JobFilter{Source: "vacancy_ingest", IncludeClosed: true, Limit: 10})
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	got := jobs[0]
	assert.Equal(t, "Senior SRE", got.Title, "title must be promoted from ok ingest")
	assert.Equal(t, "Manages oncall rotation and reliability improvements.", got.Description, "description must be promoted from ok ingest")
	assert.Equal(t, "ACME", got.Company, "company must be promoted from ok ingest")
	assert.Equal(t, []string{"go", "k8s"}, got.Skills, "skills must be promoted from ok ingest")
}

// --- AuditContest ---

func truncateAuditContests(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), "TRUNCATE hunt_audit_contests CASCADE")
	require.NoError(t, err)
}

func TestStore_UpsertAuditContest_Created(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))
	truncateAuditContests(t, pool)

	ac := hunt.AuditContest{
		DedupHash: hunt.DedupHash("https://code4rena.com/contests/2024-01-example"),
		Title:     "DeFi Protocol Audit",
		URL:       "https://code4rena.com/contests/2024-01-example",
		Platform:  "code4rena",
		TotalPool: 150000,
		Currency:  "USDC",
		Languages: []string{"solidity"},
	}

	id, outcome, err := s.UpsertAuditContest(ctx, ac)
	require.NoError(t, err)
	assert.Greater(t, id, int64(0))
	assert.Equal(t, hunt.OutcomeCreated, outcome)
}

func TestStore_UpsertAuditContest_Merged(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))
	truncateAuditContests(t, pool)

	ac := hunt.AuditContest{
		DedupHash: hunt.DedupHash("https://code4rena.com/contests/2024-02-other"),
		Title:     "Other Contest",
		URL:       "https://code4rena.com/contests/2024-02-other",
		Platform:  "sherlock",
	}

	_, outcome1, err := s.UpsertAuditContest(ctx, ac)
	require.NoError(t, err)
	assert.Equal(t, hunt.OutcomeCreated, outcome1)

	_, outcome2, err := s.UpsertAuditContest(ctx, ac)
	require.NoError(t, err)
	assert.Equal(t, hunt.OutcomeMerged, outcome2)
}

// --- Ratings ---

func truncateRatings(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), "TRUNCATE hunt_ratings CASCADE")
	require.NoError(t, err)
}

func TestStore_Rate_Insert(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))
	truncateRatings(t, pool)

	err := s.Rate(ctx, hunt.KindBounty, 1, "krolik", hunt.StageInteresting, "looks good")
	require.NoError(t, err)

	r, err := s.GetRating(ctx, hunt.KindBounty, 1, "krolik")
	require.NoError(t, err)
	assert.Equal(t, hunt.StageInteresting, r.Stage)
	assert.Equal(t, "looks good", r.Note)
}

func TestStore_Rate_Update(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))
	truncateRatings(t, pool)

	err := s.Rate(ctx, hunt.KindBounty, 2, "krolik", hunt.StageNew, "")
	require.NoError(t, err)

	err = s.Rate(ctx, hunt.KindBounty, 2, "krolik", hunt.StageSaved, "updated note")
	require.NoError(t, err)

	r, err := s.GetRating(ctx, hunt.KindBounty, 2, "krolik")
	require.NoError(t, err)
	assert.Equal(t, hunt.StageSaved, r.Stage)
	assert.Equal(t, "updated note", r.Note)
}

func TestStore_GetRating_NotFound(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))

	_, err := s.GetRating(ctx, hunt.KindBounty, 999999999, "nobody")
	assert.ErrorIs(t, err, hunt.ErrNotFound)
}

// TestListShortlist_UserIsolation asserts that a rating entered by a DIFFERENT user
// does not appear in the owner user's shortlist, even when the stage is curated.
// Red-on-revert: removing the r.user_name = $1 filter from ListShortlist → the
// foreign-user rating leaks through → assert.Empty fails.
func TestListShortlist_UserIsolation(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()

	const ownerUser = "test_sl_iso"
	const otherUser = "test_sl_iso_other"

	cleanup := func() {
		_, _ = pool.Exec(ctx, "DELETE FROM hunt_ratings WHERE user_name IN ($1, $2)", ownerUser, otherUser)
		_, _ = pool.Exec(ctx, "DELETE FROM hunt_jobs WHERE source = 'test_sl_iso'")
	}
	cleanup()
	t.Cleanup(cleanup)

	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))

	// Insert one hunt_jobs row.
	h := hunt.DedupHash("https://test.example/iso/job1")
	var jobID int64
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO hunt_jobs (dedup_hash, title, company, url, source)
		VALUES ($1, 'Iso Role', 'Iso Corp', 'https://test.example/iso/job1', 'test_sl_iso')
		ON CONFLICT (dedup_hash) DO UPDATE SET source='test_sl_iso'
		RETURNING id`, h).Scan(&jobID))

	// Rate the job under the OTHER user with a curated stage.
	require.NoError(t, s.Rate(ctx, "job", jobID, otherUser, hunt.StageSaved, ""))

	// The owner user must see zero rows — foreign rater's row must be excluded.
	rows, _, err := s.ListShortlist(ctx, hunt.ShortlistQuery{
		User:   ownerUser,
		Stages: []string{hunt.StageInteresting, hunt.StageSaved, hunt.StageClaimed, hunt.StageApplied, hunt.StageInterview, hunt.StageOffer},
	})
	require.NoError(t, err)
	assert.Empty(t, rows, "rating entered by a different user must not appear in the owner's shortlist")
}
