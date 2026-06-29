package adminui

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/anatolykoptev/go_job/internal/dbtest"
	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// rateForTest is a test-only helper that routes a logical status value to the
// correct DB axis (triage vs stage) and calls Store.Rate. Mirrors the axis-routing
// logic in trackerRate and the adminui handlers so tests exercise the real code path.
//
// Triage-axis values (hunt.TriageStages): written to triage column, stage="".
// Pipeline-axis values (hunt.PipelineStages): written to stage column, triage="".
func rateForTest(ctx context.Context, store *hunt.Store, kind string, id int64, user, value, note string) error {
	switch value {
	case hunt.StageInteresting, hunt.StageSaved, hunt.StageDiscarded:
		return store.Rate(ctx, kind, id, user, value, "", note)
	default:
		return store.Rate(ctx, kind, id, user, "", value, note)
	}
}

func openShortlistPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	dbtest.RequireTestDB(t, dsn)
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	return pool
}

func truncateShortlistData(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), "DELETE FROM hunt_ratings WHERE user_name = 'test_sl'")
	if err != nil {
		t.Fatalf("truncate ratings: %v", err)
	}
	_, err = pool.Exec(context.Background(), "DELETE FROM hunt_jobs WHERE source = 'test_sl'")
	if err != nil {
		t.Fatalf("truncate jobs: %v", err)
	}
}

// insertTestJob inserts a minimal hunt_jobs row for shortlist tests and returns its id.
func insertTestJob(t *testing.T, pool *pgxpool.Pool, company, title string, fitScore *int, fitBand, postedAt string) int64 {
	t.Helper()
	h := hunt.DedupHash("https://test.example/" + company + "/" + title)
	var id int64
	var err error
	if fitScore != nil && postedAt != "" {
		err = pool.QueryRow(context.Background(), `
			INSERT INTO hunt_jobs (dedup_hash, title, company, url, source, fit_score, fit_band, success_band, over_under, posted_at, scored_at)
			VALUES ($1, $2, $3, $4, 'test_sl', $5, $6, 'STRONG', 'well_matched', $7::date, NOW())
			ON CONFLICT (dedup_hash) DO UPDATE SET title=$2, company=$3, source='test_sl', fit_score=$5, fit_band=$6, posted_at=$7::date, scored_at=NOW()
			RETURNING id`,
			h, title, company, "https://test.example/"+company+"/"+title, *fitScore, fitBand, postedAt,
		).Scan(&id)
	} else {
		err = pool.QueryRow(context.Background(), `
			INSERT INTO hunt_jobs (dedup_hash, title, company, url, source)
			VALUES ($1, $2, $3, $4, 'test_sl')
			ON CONFLICT (dedup_hash) DO UPDATE SET title=$2, company=$3, source='test_sl'
			RETURNING id`,
			h, title, company, "https://test.example/"+company+"/"+title,
		).Scan(&id)
	}
	if err != nil {
		t.Fatalf("insertTestJob %s/%s: %v", company, title, err)
	}
	return id
}

// ── postgres integration tests ─────────────────────────────────────────────────

// TestShortlistPG_ListShortlist seeds hunt_jobs + hunt_ratings and asserts that
// ListShortlist returns only rows in the active-stage set with real fit_score.
// Red-on-revert: deleting/breaking ListShortlist → no rows returned → test fails.
func TestShortlistPG_ListShortlist(t *testing.T) {
	pool := openShortlistPool(t)
	ctx := context.Background()
	truncateShortlistData(t, pool)
	t.Cleanup(func() { truncateShortlistData(t, pool) })

	store := hunt.NewStore(pool)
	score := 85
	idA := insertTestJob(t, pool, "Acme", "Staff Eng", &score, "strong", "2026-01-15")
	score2 := 60
	idB := insertTestJob(t, pool, "Beta", "SWE II", &score2, "moderate", "2026-02-01")
	idC := insertTestJob(t, pool, "Gamma", "Reject Me", nil, "", "")

	// Rate A as "saved" (triage axis), B as "interesting" (triage axis),
	// C as "discarded" (triage axis, excluded from shortlist by shortlistTriageValues).
	if err := rateForTest(ctx, store, "job", idA, "test_sl", hunt.StageSaved, ""); err != nil {
		t.Fatalf("rate A: %v", err)
	}
	if err := rateForTest(ctx, store, "job", idB, "test_sl", hunt.StageInteresting, ""); err != nil {
		t.Fatalf("rate B: %v", err)
	}
	if err := rateForTest(ctx, store, "job", idC, "test_sl", hunt.StageDiscarded, ""); err != nil {
		t.Fatalf("rate C: %v", err)
	}

	rows, _, err := store.ListShortlist(ctx, hunt.ShortlistQuery{
		User:         "test_sl",
		TriageValues: shortlistTriageValues,
		StageValues:  shortlistPipelineValues,
	})
	if err != nil {
		t.Fatalf("ListShortlist: %v", err)
	}

	// Discarded must be excluded; A and B must appear.
	if len(rows) != 2 {
		t.Fatalf("want 2 shortlist rows, got %d (discarded must be excluded)", len(rows))
	}

	// DB orders by fit_score DESC NULLS LAST — Acme (85) before Beta (60).
	if rows[0].Company != "Acme" {
		t.Errorf("row[0] should be Acme (fit=85), got %q", rows[0].Company)
	}
	if rows[0].FitScore == nil || *rows[0].FitScore != 85 {
		t.Errorf("Acme fit_score: want 85, got %v", rows[0].FitScore)
	}
	if rows[0].FitBand != "strong" {
		t.Errorf("Acme fit_band: want strong, got %q", rows[0].FitBand)
	}
	if rows[0].PostedAt == nil {
		t.Error("Acme: PostedAt must not be nil")
	}
	if rows[1].Company != "Beta" {
		t.Errorf("row[1] should be Beta (fit=60), got %q", rows[1].Company)
	}
}

// TestShortlistPG_AllActiveStagesIncluded verifies that all 6 active
// triage+pipeline values appear on the shortlist and the 3 excluded values do not.
//
// After migration 012 the active set is split across two axes:
//   - triage: interesting, saved          (shortlistTriageValues)
//   - stage:  claimed, applied, interview, offer (shortlistPipelineValues)
//
// Excluded: discarded (triage axis, not in shortlistTriageValues),
//
//	rejected (pipeline axis, not in shortlistPipelineValues),
//	new      (legacy, maps to both-empty after migration)
//
// Red-on-revert: removing a value from either shortlistTriageValues or
// shortlistPipelineValues → row count drops → test fails.
func TestShortlistPG_AllActiveStagesIncluded(t *testing.T) {
	pool := openShortlistPool(t)
	ctx := context.Background()
	truncateShortlistData(t, pool)
	t.Cleanup(func() { truncateShortlistData(t, pool) })

	store := hunt.NewStore(pool)

	// Active values (should appear).
	activeTriageValues := []string{hunt.StageInteresting, hunt.StageSaved}
	activePipelineValues := []string{hunt.StageClaimed, hunt.StageApplied, hunt.StageInterview, hunt.StageOffer}
	totalActive := len(activeTriageValues) + len(activePipelineValues)

	for _, v := range activeTriageValues {
		id := insertTestJob(t, pool, v+"-co", v+"-role", nil, "", "")
		if err := rateForTest(ctx, store, "job", id, "test_sl", v, ""); err != nil {
			t.Fatalf("rate triage=%s: %v", v, err)
		}
	}
	for _, v := range activePipelineValues {
		id := insertTestJob(t, pool, v+"-co", v+"-role", nil, "", "")
		if err := rateForTest(ctx, store, "job", id, "test_sl", v, ""); err != nil {
			t.Fatalf("rate stage=%s: %v", v, err)
		}
	}

	// Excluded values (must not appear).
	for _, v := range []string{hunt.StageDiscarded, hunt.StageRejected} {
		id := insertTestJob(t, pool, v+"-co", v+"-role", nil, "", "")
		if err := rateForTest(ctx, store, "job", id, "test_sl", v, ""); err != nil {
			t.Fatalf("rate excluded=%s: %v", v, err)
		}
	}

	rows, _, err := store.ListShortlist(ctx, hunt.ShortlistQuery{
		User:         "test_sl",
		TriageValues: shortlistTriageValues,
		StageValues:  shortlistPipelineValues,
	})
	if err != nil {
		t.Fatalf("ListShortlist: %v", err)
	}
	if len(rows) != totalActive {
		t.Errorf("want %d active rows, got %d", totalActive, len(rows))
	}

	// Excluded must not appear.
	excluded := map[string]bool{hunt.StageDiscarded: true, hunt.StageRejected: true}
	for _, r := range rows {
		if excluded[r.Triage] || excluded[r.Stage] {
			t.Errorf("excluded value (triage=%q, stage=%q) appeared in shortlist", r.Triage, r.Stage)
		}
	}
}

// ── resource.Resource unit tests ──────────────────────────────────────────────

// TestStageBadgeHTML verifies that stageBadgeHTML uses closed-enum CSS classes,
// escapes stage text, and returns "" for the empty string (no badge).
// Red-on-revert: removing stageBadgeClass map → wrong/missing CSS class → fails.
func TestStageBadgeHTML(t *testing.T) {
	cases := []struct {
		value   string
		want    string // expected substring in output
		nonEmpty bool   // if true, output must be non-empty
	}{
		// Triage-axis badges.
		{hunt.StageInteresting, "badge-blue", true},
		{hunt.StageSaved, `class="badge"`, true},    // saved → plain badge (no extra modifier)
		{hunt.StageDiscarded, `class="badge"`, true}, // discarded → plain badge
		// Pipeline-axis badges.
		{hunt.StageClaimed, "badge-blue", true},
		{hunt.StageApplied, "badge-blue", true},
		{hunt.StageInterview, "badge-green", true},
		{hunt.StageOffer, "badge-green", true},
		// Unknown / empty.
		{"unknown-stage", `class="badge"`, true},          // unknown → plain badge
		{"<script>xss</script>", "&lt;script&gt;", true},  // must escape user-visible text
		{"", "", false},                                    // empty → no output
	}
	for _, tc := range cases {
		got := stageBadgeHTML(tc.value)
		if !tc.nonEmpty {
			if got != "" {
				t.Errorf("stageBadgeHTML(%q): want empty string, got %q", tc.value, got)
			}
			continue
		}
		if !strings.Contains(got, tc.want) {
			t.Errorf("stageBadgeHTML(%q): want %q in output, got %q", tc.value, tc.want, got)
		}
	}
}

// TestDocsChipHTML verifies the four documentation states produce download links or a dash.
// Red-on-revert: removing docsChipHTML or reverting to badge-only → href assertions fail.
func TestDocsChipHTML(t *testing.T) {
	const testID int64 = 42
	cases := []struct {
		hasResume, hasCover bool
		wantAll             []string // every string must appear in the output
	}{
		{
			hasResume: true, hasCover: true,
			wantAll: []string{
				`href="/admin/jobs/42/download/resume"`,
				`href="/admin/jobs/42/download/cover"`,
				"badge-green",
			},
		},
		{
			hasResume: true, hasCover: false,
			wantAll: []string{
				`href="/admin/jobs/42/download/resume"`,
				"Resume",
			},
		},
		{
			hasResume: false, hasCover: true,
			wantAll: []string{
				`href="/admin/jobs/42/download/cover"`,
				"Cover",
			},
		},
		{
			hasResume: false, hasCover: false,
			wantAll: []string{"badge-gray"},
		},
	}
	for _, tc := range cases {
		got := docsChipHTML(testID, tc.hasResume, tc.hasCover)
		for _, want := range tc.wantAll {
			if !strings.Contains(got, want) {
				t.Errorf("docsChipHTML(id=%d, resume=%v, cover=%v): want %q in output, got %q",
					testID, tc.hasResume, tc.hasCover, want, got)
			}
		}
	}
}
