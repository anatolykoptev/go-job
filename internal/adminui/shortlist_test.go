package adminui

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func openShortlistPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping shortlist integration tests")
	}
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

	// Rate A as "saved", B as "interesting", C as "discarded" (excluded from shortlist).
	if err := store.Rate(ctx, "job", idA, "test_sl", hunt.StageSaved, ""); err != nil {
		t.Fatalf("rate A: %v", err)
	}
	if err := store.Rate(ctx, "job", idB, "test_sl", hunt.StageInteresting, ""); err != nil {
		t.Fatalf("rate B: %v", err)
	}
	if err := store.Rate(ctx, "job", idC, "test_sl", hunt.StageDiscarded, ""); err != nil {
		t.Fatalf("rate C: %v", err)
	}

	rows, _, err := store.ListShortlist(ctx, hunt.ShortlistQuery{User: "test_sl", Stages: shortlistActiveStages})
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

// TestShortlistPG_AllActiveStagesIncluded verifies all 6 active stages reach the shortlist
// and the 3 excluded stages (new, discarded, rejected) do not.
// Red-on-revert: removing a stage from shortlistActiveStages → row count drops → test fails.
func TestShortlistPG_AllActiveStagesIncluded(t *testing.T) {
	pool := openShortlistPool(t)
	ctx := context.Background()
	truncateShortlistData(t, pool)
	t.Cleanup(func() { truncateShortlistData(t, pool) })

	store := hunt.NewStore(pool)

	// One job per stage.
	activeStages := []string{
		hunt.StageInteresting, hunt.StageSaved, hunt.StageClaimed,
		hunt.StageApplied, hunt.StageInterview, hunt.StageOffer,
	}
	excludedStages := []string{hunt.StageNew, hunt.StageDiscarded, hunt.StageRejected}

	for _, stage := range append(activeStages, excludedStages...) {
		id := insertTestJob(t, pool, stage+"-co", stage+"-role", nil, "", "")
		if err := store.Rate(ctx, "job", id, "test_sl", stage, ""); err != nil {
			t.Fatalf("rate stage=%s: %v", stage, err)
		}
	}

	rows, _, err := store.ListShortlist(ctx, hunt.ShortlistQuery{User: "test_sl", Stages: shortlistActiveStages})
	if err != nil {
		t.Fatalf("ListShortlist: %v", err)
	}
	if len(rows) != len(activeStages) {
		t.Errorf("want %d rows (active stages), got %d", len(activeStages), len(rows))
	}

	// None of the excluded stages should appear.
	excluded := map[string]bool{hunt.StageNew: true, hunt.StageDiscarded: true, hunt.StageRejected: true}
	for _, r := range rows {
		if excluded[r.Stage] {
			t.Errorf("excluded stage %q appeared in shortlist", r.Stage)
		}
	}
}

// ── resource.Resource unit tests ──────────────────────────────────────────────

// TestStageBadgeHTML verifies that stageBadgeHTML uses closed-enum CSS classes and
// escapes stage text, with no raw DB text in HTML attribute values.
// Red-on-revert: removing stageBadgeClass map → wrong/missing CSS class → fails.
func TestStageBadgeHTML(t *testing.T) {
	cases := []struct {
		stage   string
		wantCls string // expected substring in output
	}{
		{hunt.StageInteresting, "badge-blue"},
		{hunt.StageSaved, `class="badge"`},        // no extra modifier for saved
		{hunt.StageClaimed, "badge-blue"},
		{hunt.StageApplied, "badge-blue"},
		{hunt.StageInterview, "badge-green"},
		{hunt.StageOffer, "badge-green"},
		{"unknown-stage", `class="badge"`},         // unknown → plain badge, no modifier
		{"<script>xss</script>", "&lt;script&gt;"}, // stage text must be escaped
	}
	for _, tc := range cases {
		got := stageBadgeHTML(tc.stage)
		if !strings.Contains(got, tc.wantCls) {
			t.Errorf("stageBadgeHTML(%q): want %q in output, got %q", tc.stage, tc.wantCls, got)
		}
	}
}

// TestDocsChipHTML verifies the four documentation states produce distinct badges.
// Red-on-revert: removing docsChipHTML or altering its switch → wrong badge class → fails.
func TestDocsChipHTML(t *testing.T) {
	cases := []struct {
		hasResume, hasCover bool
		wantContains        string
	}{
		{true, true, "badge-green"},
		{true, false, "Resume"},
		{false, true, "Cover"},
		{false, false, "badge-gray"},
	}
	for _, tc := range cases {
		got := docsChipHTML(tc.hasResume, tc.hasCover)
		if !strings.Contains(got, tc.wantContains) {
			t.Errorf("docsChipHTML(resume=%v, cover=%v): want %q, got %q",
				tc.hasResume, tc.hasCover, tc.wantContains, got)
		}
	}
}
