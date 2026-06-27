package adminui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

	rows, err := store.ListShortlist(ctx, "test_sl", shortlistActiveStages)
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

	rows, err := store.ListShortlist(ctx, "test_sl", shortlistActiveStages)
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

// TestShortlistPG_EnrichPackReady verifies PDF-derived pack-ready is computed from
// filesystem presence, not from a DB column.
// Red-on-revert: removing PDF scan from enrichPGShortlist → pack-ready always false → fails.
func TestShortlistPG_EnrichPackReady(t *testing.T) {
	root := t.TempDir()

	// Beta has a resume PDF in the submit/ canonical location.
	submit := filepath.Join(root, "beta-engineer", "submit")
	if err := os.MkdirAll(submit, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(submit, "resume.pdf"), []byte("%PDF"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(submit, "cover.pdf"), []byte("%PDF"), 0o644); err != nil {
		t.Fatal(err)
	}

	fitScore := 72
	rows := []hunt.ShortlistRow{
		{ID: 1, Company: "Acme", Title: "SWE", Stage: hunt.StageSaved},
		{ID: 2, Company: "Beta", Title: "Engineer", FitScore: &fitScore, FitBand: "moderate", Stage: hunt.StageSaved},
	}

	entries := enrichPGShortlist(rows, root)
	if len(entries) != 2 {
		t.Fatalf("want 2 entries, got %d", len(entries))
	}

	// Beta (pack-ready) must sort first.
	if entries[0].Company != "Beta" {
		t.Errorf("pack-ready Beta should sort first, got %q", entries[0].Company)
	}
	if !entries[0].HasResume {
		t.Error("Beta: HasResume must be true")
	}
	if !entries[0].HasCover {
		t.Error("Beta: HasCover must be true")
	}
	if !entries[0].PackReady {
		t.Error("Beta: PackReady must be true (HasResume && HasCover)")
	}
	if entries[1].PackReady {
		t.Error("Acme: PackReady must be false (no PDFs)")
	}
}

// TestShortlistPG_RenderHTMLFitChips verifies that renderPGShortlistHTML produces
// HTML containing the real fit chip HTML and stage badges, not stale tracker score.
// Red-on-revert: remove renderPGShortlistHTML or fitChipHTML → missing fit chip → fails.
func TestShortlistPG_RenderHTMLFitChips(t *testing.T) {
	fitScore := 85
	postTime := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	rows := []hunt.ShortlistRow{
		{
			ID:       1,
			Company:  "Anthropic",
			Title:    "Staff Engineer",
			FitScore: &fitScore,
			FitBand:  "strong",
			Stage:    hunt.StageSaved,
			PostedAt: &postTime,
		},
	}
	entries := enrichPGShortlist(rows, t.TempDir())
	html := renderPGShortlistHTML(entries, "")

	// Must contain the fit chip HTML produced by fitChipHTML.
	if !strings.Contains(html, "fit-strong") {
		t.Error("HTML must contain fit-strong chip CSS class")
	}
	if !strings.Contains(html, "85") {
		t.Error("HTML must contain the fit score value 85")
	}
	// Stage badge must appear.
	if !strings.Contains(html, "saved") {
		t.Error("HTML must contain the stage badge")
	}
	// Posted date must appear.
	if !strings.Contains(html, "2026-01-15") {
		t.Error("HTML must contain the posted date")
	}
	// Filter chip counts.
	if !strings.Contains(html, "All 1") {
		t.Error("HTML must contain All 1 filter chip")
	}
	if !strings.Contains(html, "Saved 1") {
		t.Error("HTML must contain Saved 1 filter chip")
	}
}

// TestShortlistPG_FilterPackReady verifies that the pack-ready filter shows only
// entries where HasResume && HasCover (derived, not a DB column).
// Red-on-revert: remove filter logic in renderPGShortlistHTML → wrong filter → fails.
func TestShortlistPG_FilterPackReady(t *testing.T) {
	fitScore := 70
	rows := []hunt.ShortlistRow{
		{ID: 1, Company: "Acme", Title: "SWE", FitScore: &fitScore, FitBand: "moderate", Stage: hunt.StageSaved},
		{ID: 2, Company: "Beta", Title: "Eng", Stage: hunt.StageInteresting},
	}
	root := t.TempDir()
	// Give Acme a PDF directory so it becomes pack-ready.
	submit := filepath.Join(root, "acme-swe", "submit")
	if err := os.MkdirAll(submit, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(submit, "resume.pdf"), []byte("%PDF"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(submit, "cover.pdf"), []byte("%PDF"), 0o644); err != nil {
		t.Fatal(err)
	}

	entries := enrichPGShortlist(rows, root)
	htmlAll := renderPGShortlistHTML(entries, "")
	htmlPR := renderPGShortlistHTML(entries, "pack-ready")

	if !strings.Contains(htmlAll, "Acme") || !strings.Contains(htmlAll, "Beta") {
		t.Error("unfiltered view must show both entries")
	}
	if !strings.Contains(htmlPR, "Acme") {
		t.Error("pack-ready filter must include Acme (has resume+cover)")
	}
	if strings.Contains(htmlPR, ">Beta<") {
		t.Error("pack-ready filter must exclude Beta (no PDFs)")
	}
}

// ── JSON fallback tests (existing path — kept for rollback guarantee) ──────────

func TestShortlist_EnrichAndSort(t *testing.T) {
	root := t.TempDir()
	tracker := `{"version":1,"updated":"2026-06-11","jobs":[
		{"score":10,"company":"Acme","title":"Platform Engineer","status":"saved","url":"https://x"},
		{"score":16,"company":"Beta","title":"Staff Engineer","status":"pack-ready","url":"https://y"}
	]}`
	if err := os.WriteFile(filepath.Join(root, "_tracker.json"), []byte(tracker), 0o644); err != nil {
		t.Fatal(err)
	}
	// Beta (pack-ready) has a prepared resume PDF; Acme has none.
	submit := filepath.Join(root, "beta-staff", "submit")
	if err := os.MkdirAll(submit, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(submit, "resume.pdf"), []byte("%PDF"), 0o644); err != nil {
		t.Fatal(err)
	}

	tf, err := loadTracker(root)
	if err != nil {
		t.Fatalf("loadTracker: %v", err)
	}
	entries := enrichShortlist(tf.Jobs, root)
	if len(entries) != 2 {
		t.Fatalf("want 2 entries, got %d", len(entries))
	}
	// pack-ready sorts before saved
	if entries[0].Company != "Beta" {
		t.Errorf("pack-ready should sort first, got %q", entries[0].Company)
	}
	if entries[0].Slug != "beta-staff" || !entries[0].HasResume {
		t.Errorf("Beta should resolve beta-staff with a resume PDF; got slug=%q hasResume=%v", entries[0].Slug, entries[0].HasResume)
	}
	if entries[0].HasCover {
		t.Error("Beta has no cover PDF")
	}
	if entries[1].HasResume || entries[1].HasCover {
		t.Error("Acme has no prepared pack → no PDFs")
	}

	// rendering does not panic and includes the curated company.
	html := renderShortlistHTML(tf, entries, "")
	if !strings.Contains(html, "Beta") || !strings.Contains(html, "/admin/shortlist/beta-staff/download/resume") {
		t.Errorf("rendered shortlist missing expected content")
	}
}

func TestLoadTracker_Missing(t *testing.T) {
	if _, err := loadTracker(t.TempDir()); err == nil {
		t.Error("expected error when _tracker.json absent")
	}
}

func TestShortlist_FilterCounts(t *testing.T) {
	entries := []shortlistEntry{
		{trackerJob: trackerJob{Company: "Acme", Status: "pack-ready"}, HasResume: true},
		{trackerJob: trackerJob{Company: "Beta", Status: "saved"}},
		{trackerJob: trackerJob{Company: "Gamma", Status: "saved"}},
	}
	tf := &trackerFile{Updated: "2026-06-11"}

	all := renderShortlistHTML(tf, entries, "")
	for _, want := range []string{"All 3", "With docs 1", "Pack-ready 1", "Saved 2"} {
		if !strings.Contains(all, want) {
			t.Errorf("chip counts missing %q", want)
		}
	}
	saved := renderShortlistHTML(tf, entries, "saved")
	if strings.Contains(saved, ">Acme<") {
		t.Error("saved filter must exclude pack-ready Acme")
	}
	if !strings.Contains(saved, ">Beta<") || !strings.Contains(saved, ">Gamma<") {
		t.Error("saved filter must include Beta and Gamma")
	}
	docs := renderShortlistHTML(tf, entries, "docs")
	if !strings.Contains(docs, ">Acme<") || strings.Contains(docs, ">Beta<") {
		t.Error("docs filter must include only Acme")
	}
}

// TestShortlist_SourceFlagJSON verifies that shortlistActiveStages and the
// JSON-path functions co-exist correctly: loadTracker still works when
// SHORTLIST_SOURCE=json is set (the rollback lever). This test does NOT call the
// handler (requires a real Panel), but validates the JSON path functions that the
// handler dispatches to.
// Red-on-revert: remove loadTracker → function missing → compile error / test fails.
func TestShortlist_SourceFlagJSON(t *testing.T) {
	root := t.TempDir()
	tracker := `{"version":1,"updated":"2026-06-25","jobs":[
		{"score":8,"company":"Rollback","title":"Eng","status":"saved","url":"https://rb"}
	]}`
	if err := os.WriteFile(filepath.Join(root, "_tracker.json"), []byte(tracker), 0o644); err != nil {
		t.Fatal(err)
	}

	// Simulate the handler's json-path branch.
	t.Setenv("SHORTLIST_SOURCE", "json")
	if os.Getenv("SHORTLIST_SOURCE") != "json" {
		t.Skip("Setenv not reflected — skip")
	}

	tf, err := loadTracker(root)
	if err != nil {
		t.Fatalf("loadTracker (json fallback path): %v", err)
	}
	if len(tf.Jobs) != 1 || tf.Jobs[0].Company != "Rollback" {
		t.Errorf("json fallback: unexpected jobs: %+v", tf.Jobs)
	}
	entries := enrichShortlist(tf.Jobs, root)
	html := renderShortlistHTML(tf, entries, "")
	if !strings.Contains(html, "Rollback") {
		t.Error("json fallback rendered HTML missing Rollback company")
	}
}
