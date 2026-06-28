package hunt_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/anatolykoptev/go_job/internal/dbtest"
	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// openTestPool returns a pool for integration tests.
// Skips if DATABASE_URL is unset; fatals if it points at a non-_test database.
func openTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	dbtest.RequireTestDB(t, dsn)

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

// TestStore_UpsertJob_NoDowngrade verifies that a weak re-ingest (title=”) over an
// already-good row NEVER overwrites the good fields. Locks the ELSE arm: once title is
// non-empty the row is good and the ELSE branch preserves ALL content fields regardless
// of what EXCLUDED carries. NOTE: this test does NOT lock the EXCLUDED.* <> ”/IS NOT NULL
// guards; those are covered by TestStore_UpsertJob_WeakRow_NoFieldDowngrade below.
func TestStore_UpsertJob_NoDowngrade(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))
	truncateJobs(t, pool)

	url := "https://jobs.example.com/nodowngrade-vacancy"
	hash := hunt.DedupHash(url)

	// Step 1: good ingest — complete data from LLM.
	good := hunt.Job{
		DedupHash:   hash,
		Title:       "Principal SRE",
		Company:     "GoodCorp",
		URL:         url,
		Source:      "vacancy_ingest",
		Description: "Own reliability for 99.99% uptime across 50 services.",
		Skills:      []string{"go", "prometheus", "k8s"},
	}
	_, outcome1, err := s.UpsertJob(ctx, good)
	require.NoError(t, err)
	assert.Equal(t, hunt.OutcomeCreated, outcome1, "good ingest should create the row")

	// Step 2: weak re-ingest of the same URL — LLM returned nothing.
	weak := hunt.Job{
		DedupHash:   hash,
		Title:       "", // empty — hallmark of weak ingest
		Company:     "",
		URL:         url,
		Source:      "vacancy_ingest",
		Description: "<html><body>raw html blob</body></html>",
		Skills:      nil,
	}
	_, outcome2, err := s.UpsertJob(ctx, weak)
	require.NoError(t, err)
	assert.Equal(t, hunt.OutcomeMerged, outcome2, "weak re-ingest should merge")

	// Good fields must be preserved — weak re-ingest must NOT downgrade them.
	jobs, err := s.ListJobs(ctx, hunt.JobFilter{Source: "vacancy_ingest", IncludeClosed: true, Limit: 10})
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	got := jobs[0]
	assert.Equal(t, "Principal SRE", got.Title, "good title must survive weak re-ingest")
	assert.Equal(t, "Own reliability for 99.99% uptime across 50 services.", got.Description, "good description must not be overwritten by HTML blob")
	assert.Equal(t, "GoodCorp", got.Company, "good company must survive empty re-ingest")
	assert.Equal(t, []string{"go", "prometheus", "k8s"}, got.Skills, "good skills must not be cleared by nil re-ingest")
}

// TestStore_UpsertJob_WeakRow_NoFieldDowngrade locks the EXCLUDED.* <> ” / IS NOT NULL
// guards in UpsertJob ON CONFLICT that protect populated content fields even when the
// stored row is weak (title=”). A second weak re-ingest with empty description/company
// and nil skills must NOT overwrite the step-1 populated values.
//
// RED-on-revert: removing any of these guards from the ON CONFLICT SET makes this test fail:
//
//	EXCLUDED.description IS NOT NULL AND EXCLUDED.description <> ''
//	EXCLUDED.company IS NOT NULL AND EXCLUDED.company <> ''
//	array_length(EXCLUDED.skills, 1) IS NOT NULL
func TestStore_UpsertJob_WeakRow_NoFieldDowngrade(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))
	truncateJobs(t, pool)

	url := "https://jobs.example.com/weak-nodowngrade-vacancy"
	hash := hunt.DedupHash(url)

	// Step 1: create a WEAK row (title='') but with non-empty description/company/skills.
	weak1 := hunt.Job{
		DedupHash:   hash,
		Title:       "", // empty — the weak-row marker; title='' is the queryable proxy
		Company:     "PartialCorp",
		URL:         url,
		Source:      "vacancy_ingest",
		Description: "Some partial description from raw scrape.",
		Skills:      []string{"python", "docker"},
	}
	_, outcome1, err := s.UpsertJob(ctx, weak1)
	require.NoError(t, err)
	assert.Equal(t, hunt.OutcomeCreated, outcome1, "step 1 should create the weak row")

	// Step 2: still-weak re-ingest (title still '') with EMPTY description, EMPTY company,
	// and nil skills. The EXCLUDED.* <> '' / IS NOT NULL guards must block clobber.
	weak2 := hunt.Job{
		DedupHash:   hash,
		Title:       "", // still weak
		Company:     "", // incoming empty — must NOT overwrite "PartialCorp"
		URL:         url,
		Source:      "vacancy_ingest",
		Description: "",  // incoming empty — must NOT overwrite step-1 description
		Skills:      nil, // incoming nil  — must NOT clear step-1 skills
	}
	_, outcome2, err := s.UpsertJob(ctx, weak2)
	require.NoError(t, err)
	assert.Equal(t, hunt.OutcomeMerged, outcome2, "step 2 should merge (same URL)")

	// Step-1 non-empty fields must survive the step-2 empty re-ingest.
	jobs, err := s.ListJobs(ctx, hunt.JobFilter{Source: "vacancy_ingest", IncludeClosed: true, Limit: 10})
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	got := jobs[0]
	assert.Equal(t, "Some partial description from raw scrape.", got.Description,
		"EXCLUDED.description guard: non-empty stored description must survive empty re-ingest on weak row")
	assert.Equal(t, "PartialCorp", got.Company,
		"EXCLUDED.company guard: non-empty stored company must survive empty re-ingest on weak row")
	assert.Equal(t, []string{"python", "docker"}, got.Skills,
		"EXCLUDED.skills guard: non-nil stored skills must survive nil re-ingest on weak row")
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

// ── CountScored + CountBySource (Phase 4a dashboard) ─────────────────────────

// TestStore_CountScored_OnlyOpenAndScored seeds open-scored, open-unscored,
// and closed-scored rows. Assert CountScored returns only the open-AND-scored count.
//
// RED-on-revert: removing "AND scored_at IS NOT NULL" from CountScored's query
// makes the result include unscored rows and the assertion fails.
func TestStore_CountScored_OnlyOpenAndScored(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))
	truncateJobs(t, pool)

	mustUpsert := func(url, source, status string) int64 {
		t.Helper()
		j := hunt.Job{
			DedupHash: hunt.DedupHash(url),
			Title:     "Job " + url,
			URL:       url,
			Source:    source,
			Status:    status,
		}
		id, _, err := s.UpsertJob(ctx, j)
		require.NoError(t, err)
		return id
	}
	score := func(id int64) {
		t.Helper()
		require.NoError(t, s.SetJobScore(ctx, id, hunt.ScoreResult{
			FitBand:     "moderate",
			SuccessBand: "MODERATE",
			OverUnder:   "well_matched",
			ScoredAt:    time.Now(),
		}))
	}

	// open + scored -> counted
	idA := mustUpsert("https://example.com/jobs/a", "linkedin", hunt.StatusOpen)
	score(idA)
	// open + unscored -> NOT counted
	_ = mustUpsert("https://example.com/jobs/b", "indeed", hunt.StatusOpen)
	// closed + scored -> NOT counted (status!='open')
	idC := mustUpsert("https://example.com/jobs/c", "linkedin", hunt.StatusOpen)
	score(idC)
	_, err := pool.Exec(ctx, "UPDATE hunt_jobs SET status='closed' WHERE id=$1", idC)
	require.NoError(t, err)

	got := s.CountScored(ctx)
	assert.Equal(t, 1, got, "CountScored must count only open rows with scored_at IS NOT NULL")
}

// TestStore_CountBySource_DescendingOrder seeds open jobs from multiple sources
// and asserts CountBySource returns each source with the correct count, ordered
// descending by count.
//
// RED-on-revert: removing ORDER BY 2 DESC breaks the ordering assertion.
// RED-on-revert: removing WHERE status='open' includes closed rows and breaks counts.
func TestStore_CountBySource_DescendingOrder(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))
	truncateJobs(t, pool)

	seed := func(urlSuffix, source, status string) {
		t.Helper()
		j := hunt.Job{
			DedupHash: hunt.DedupHash("https://example.com/src-test/" + urlSuffix),
			Title:     "Job " + urlSuffix,
			URL:       "https://example.com/src-test/" + urlSuffix,
			Source:    source,
			Status:    status,
		}
		_, _, err := s.UpsertJob(ctx, j)
		require.NoError(t, err)
	}

	// linkedin: 3 open, indeed: 2 open, himalayas: 1 open 1 closed
	seed("l1", "linkedin", hunt.StatusOpen)
	seed("l2", "linkedin", hunt.StatusOpen)
	seed("l3", "linkedin", hunt.StatusOpen)
	seed("i1", "indeed", hunt.StatusOpen)
	seed("i2", "indeed", hunt.StatusOpen)
	seed("h1", "himalayas", hunt.StatusOpen)
	seed("h2", "himalayas", hunt.StatusOpen)
	// Mark h2 closed so only 1 himalayas open
	_, err := pool.Exec(ctx, "UPDATE hunt_jobs SET status='closed' WHERE url='https://example.com/src-test/h2'")
	require.NoError(t, err)

	rows := s.CountBySource(ctx)
	require.Len(t, rows, 3, "expect 3 distinct sources with open jobs")
	assert.Equal(t, "linkedin", rows[0].Source, "first source must be linkedin (count=3)")
	assert.Equal(t, 3, rows[0].N)
	assert.Equal(t, "indeed", rows[1].Source, "second source must be indeed (count=2)")
	assert.Equal(t, 2, rows[1].N)
	assert.Equal(t, "himalayas", rows[2].Source, "third source must be himalayas (count=1)")
	assert.Equal(t, 1, rows[2].N)
}
