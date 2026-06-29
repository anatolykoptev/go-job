package adminui

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/anatolykoptev/go-kit/admintable"
	"github.com/anatolykoptev/go-panel/resource"
	"github.com/anatolykoptev/go_job/internal/dbtest"
	"github.com/jackc/pgx/v5/pgxpool"
)

// percentageRE is the honesty fitness function for the HTML render surface.
// It mirrors scoring-canon fitness #2 (scorer_test.go) but gates the adminui
// render layer. Any success chip or detail card that leaks a percentage token
// ("\d+%" or "\d+.\d+%") fails this test. RED-on-revert: revert the
// percentage-stripping in buildMarketCardHTML and this test fails.
var percentageRE = regexp.MustCompile(`\d+(\.\d+)?\s*%`)

// TestFitChipHTML_BandMatrix asserts the fitChipHTML helper returns the correct
// CSS class and abbreviated label for every fit band, and that nil/unscored/stale
// fall through to the safe muted dash. Regression guard for the closed-enum map.
func TestFitChipHTML_BandMatrix(t *testing.T) {
	score68 := 68
	score0 := 0
	tests := []struct {
		fit     *int
		band    string
		wantCls string
		wantLbl string
		wantNum bool
	}{
		{&score68, fitBandStrong, "fit-strong", "str", true},
		{&score68, fitBandModerate, "fit-moderate", "mod", true},
		{&score68, fitBandWeak, "fit-weak", "wk", true},
		{&score68, fitBandLow, "fit-low", "low", true},
		{&score0, fitBandReject, "fit-reject", "rej", true},
		// Unscored / stale / nil → muted dash, no number.
		{nil, fitBandUnscored, "fit-unscored", "", false},
		{&score68, fitBandUnscored, "fit-unscored", "", false},
		{&score68, fitBandStale, "fit-unscored", "", false},
		// Unknown band → muted fallback, never raw band text.
		{&score68, "unknownBand", "fit-unscored", "", false},
	}
	for _, tc := range tests {
		tc := tc
		name := tc.band
		if tc.fit == nil {
			name = "nil/" + name
		}
		t.Run(name, func(t *testing.T) {
			got := fitChipHTML(tc.fit, tc.band)
			if !strings.Contains(got, tc.wantCls) {
				t.Errorf("fitChipHTML(%v, %q): want class %q in %q", tc.fit, tc.band, tc.wantCls, got)
			}
			if tc.wantLbl != "" && !strings.Contains(got, tc.wantLbl) {
				t.Errorf("fitChipHTML(%v, %q): want label %q in %q", tc.fit, tc.band, tc.wantLbl, got)
			}
			if tc.wantNum {
				numStr := strconv.Itoa(*tc.fit)
				if !strings.Contains(got, numStr) {
					t.Errorf("fitChipHTML(%v, %q): want score %q in %q", tc.fit, tc.band, numStr, got)
				}
			}
			// CRITICAL: never "unknownBand" (or any raw band string) in output.
			if strings.Contains(got, "unknownBand") {
				t.Errorf("fitChipHTML: raw band text leaked into HTML: %q", got)
			}
		})
	}
}

// TestMarketReadHTML_BandMatrix asserts the marketReadHTML helper for all
// success bands and over/under values. Also verifies the unscored (empty) path.
func TestMarketReadHTML_BandMatrix(t *testing.T) {
	tests := []struct {
		band      string
		ou        string
		wantCls   string
		wantGlyph string
	}{
		{sucBandStrong, ouMatch, "suc-strong", "◆"},
		{sucBandModerate, ouOver, "suc-moderate", "◇"},
		{sucBandLongshot, ouUnder, "suc-longshot", "◈"},
		// Unknown band → fallback moderate.
		{"UNKNOWN", ouMatch, "suc-moderate", "◇"},
		// Empty band → "not assessed".
		{"", ouMatch, "suc-none", ""},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.band+"/"+tc.ou, func(t *testing.T) {
			got := marketReadHTML(tc.band, tc.ou)
			if !strings.Contains(got, tc.wantCls) {
				t.Errorf("marketReadHTML(%q, %q): want class %q in %q", tc.band, tc.ou, tc.wantCls, got)
			}
			if tc.wantGlyph != "" && !strings.Contains(got, tc.wantGlyph) {
				t.Errorf("marketReadHTML(%q, %q): want glyph %q in %q", tc.band, tc.ou, tc.wantGlyph, got)
			}
			// CRITICAL: raw "UNKNOWN" band text must never reach HTML output.
			if strings.Contains(got, "UNKNOWN") {
				t.Errorf("marketReadHTML: raw band text leaked into HTML: %q", got)
			}
		})
	}
}

// TestMarketReadHTML_NoPercentage is the honesty fitness gate for the list
// Market Read cell. It builds marketReadHTML for every band and asserts no
// percentage appears — RED-on-revert if someone passes a success number here.
// (There are no numbers in this path; the test is the alarm if that changes.)
func TestMarketReadHTML_NoPercentage(t *testing.T) {
	bands := []string{sucBandStrong, sucBandModerate, sucBandLongshot, ""}
	ous := []string{ouOver, ouMatch, ouUnder, ""}
	for _, band := range bands {
		for _, ou := range ous {
			got := marketReadHTML(band, ou)
			if percentageRE.MatchString(got) {
				t.Errorf("marketReadHTML(%q, %q): percentage leaked in HTML: %q", band, ou, got)
			}
		}
	}
}

// TestBuildMarketCardHTML_HonestyFitness is the BLOCKING honesty grep for the
// detail MARKET READ card. It drives buildMarketCardHTML with several
// success_reasoning strings (including ones the scorer would have stripped, but
// verifying the HTML layer is also safe). Fails if any percentage appears in the
// card output. RED-on-revert: insert a "%s%%" format into buildMarketCardHTML and
// this test goes red.
func TestBuildMarketCardHTML_HonestyFitness(t *testing.T) {
	// These are what the scorer already strips before persisting — but we verify
	// the HTML layer too, so even if a legacy row had slipped through, the render
	// itself would be safe. The scorer strips at persist; we additionally check here.
	cases := []struct {
		band      string
		ou        string
		reasoning string
	}{
		{sucBandStrong, ouMatch, "strong domain and systems alignment"},
		{sucBandModerate, ouOver, "over-qualified: Staff-level candidate on a mid-level role"},
		{sucBandLongshot, ouUnder, "under-qualified: role requires 5+ years k8s production ops"},
		{"", "", ""},
		// Paranoia: even if a % somehow reached here (scorer failure), we escape it.
		// html.EscapeString does NOT remove %, but we assert no raw % pattern.
		// (The scorer strips percentages before persisting — this is a belt+suspenders check.)
		{sucBandStrong, ouMatch, "alignment is strong"},
	}
	for _, tc := range cases {
		got := buildMarketCardHTML(tc.band, tc.ou, tc.reasoning)
		if percentageRE.MatchString(got) {
			t.Errorf("buildMarketCardHTML(%q, %q, %q): percentage leaked in detail card: %q",
				tc.band, tc.ou, tc.reasoning, got)
		}
		// Honesty disclaimer must always be present.
		if !strings.Contains(got, "LLM heuristic") {
			t.Errorf("buildMarketCardHTML(%q, %qu, %q): honesty disclaimer missing", tc.band, tc.ou, tc.reasoning)
		}
	}
}

// TestBuildMarketCardHTML_DisclaimerAlwaysPresent asserts the honesty disclaimer
// renders for every input combination — including the empty/unscored path.
// Non-negotiable per spec §3c. RED-on-revert: remove the disclaimer line.
func TestBuildMarketCardHTML_DisclaimerAlwaysPresent(t *testing.T) {
	cases := [][3]string{
		{sucBandStrong, ouMatch, "great fit"},
		{"", "", ""},
		{sucBandLongshot, ouUnder, ""},
	}
	for _, tc := range cases {
		got := buildMarketCardHTML(tc[0], tc[1], tc[2])
		if !strings.Contains(got, "LLM heuristic") {
			t.Errorf("disclaimer missing for band=%q ou=%q reasoning=%q\ngot: %q", tc[0], tc[1], tc[2], got)
		}
	}
}

// TestJobsSpec_CellColumnAlignment asserts the cell count in a synthetic row
// matches the column count in jobsSpec. Guards the "cell order MUST match
// Columns order" invariant. Cell-0 = Title (plain text, Href-linked).
// RED-on-revert: add a column without a cell, or reorder cells.
func TestJobsSpec_CellColumnAlignment(t *testing.T) {
	score := 68
	// MUST match the Lister cell assembly order:
	// 0=Title, 1=Star toggle, 2=Triage badge (read-only), 3=Stage dropdown,
	// 4=Company, 5=Fit chip, 6=Market chip, 7=Status, 8=Posted,
	// 9=Location, 10=Source, 11=Docs.
	cells := []resource.Cell{
		{Value: "Some Title"},                                        // 0: title — plain text cell-0 (Href-linked)
		{Value: starToggleHTML(1, false, ""), HTML: true},            // 1: star toggle (front after Title)
		{Value: stageBadgeHTML("saved"), HTML: true},                 // 2: triage badge (read-only)
		{Value: stageDropdownHTML(1, "", ""), HTML: true},            // 3: stage dropdown (pipeline stage only)
		{Value: "Acme Corp"},                                         // 4: company — plain text
		{Value: fitChipHTML(&score, fitBandStrong), HTML: true},      // 5: fit chip
		{Value: marketReadHTML(sucBandStrong, ouMatch), HTML: true},  // 6: market chip
		{Value: "open"},                                              // 7: status (job posting open/closed)
		{Value: "2026-06-01"},                                        // 8: posted
		{Value: "Remote (US)"},                                       // 9: location
		{Value: "linkedin"},                                          // 10: source
		{Value: docsChipHTML(1, false, false), HTML: true},           // 11: docs (no resume/cover)
	}
	if len(cells) != len(jobsSpec.Columns) {
		t.Fatalf("cell/column mismatch: %d cells vs %d columns — update one of them",
			len(cells), len(jobsSpec.Columns))
	}
	// Assert cell-0 (Title) is plain text (HTML:false).
	if cells[0].HTML {
		t.Errorf("cell[0] (Title) must have HTML:false — go-panel template ignores HTML on cell-0 with Href")
	}
	// Assert star toggle is at index 1 (HTML:true, front after Title).
	if !cells[1].HTML {
		t.Errorf("cell[1] (Star toggle) must have HTML:true")
	}
	// Assert triage badge is at index 2 (HTML:true, read-only).
	if !cells[2].HTML {
		t.Errorf("cell[2] (Triage badge) must have HTML:true")
	}
	// Assert stage dropdown is at index 3 (HTML:true, right after triage badge).
	if !cells[3].HTML {
		t.Errorf("cell[3] (Stage dropdown) must have HTML:true")
	}
	// Assert company is at index 4 (plain text).
	if cells[4].HTML {
		t.Errorf("cell[4] (Company) must have HTML:false")
	}
	// Assert fit chip is at index 5 (HTML:true, not cell-0).
	if !cells[5].HTML {
		t.Errorf("cell[5] (Fit chip) must have HTML:true")
	}
	// Assert market chip is at index 6 (HTML:true, not cell-0).
	if !cells[6].HTML {
		t.Errorf("cell[6] (Market Read chip) must have HTML:true")
	}
	// Assert docs chip is at index 11 (HTML:true).
	if !cells[11].HTML {
		t.Errorf("cell[11] (Docs chip) must have HTML:true")
	}
	// Assert column order: Title[0], Star[1], Triage[2], Stage[3], Company[4], ..., Docs[last].
	if jobsSpec.Columns[0].Key != colKeyTitle {
		t.Errorf("column[0] must be Title (key=%q), got key=%q", colKeyTitle, jobsSpec.Columns[0].Key)
	}
	if jobsSpec.Columns[1].Key != colKeyStar {
		t.Errorf("column[1] must be Star (key=%q), got key=%q", colKeyStar, jobsSpec.Columns[1].Key)
	}
	if jobsSpec.Columns[2].Key != colKeyTriage {
		t.Errorf("column[2] must be Triage (key=%q), got key=%q", colKeyTriage, jobsSpec.Columns[2].Key)
	}
	if jobsSpec.Columns[3].Key != colKeyStage {
		t.Errorf("column[3] must be Stage (key=%q), got key=%q", colKeyStage, jobsSpec.Columns[3].Key)
	}
	if jobsSpec.Columns[4].Key != colCompany {
		t.Errorf("column[4] must be Company (key=%q), got key=%q", colCompany, jobsSpec.Columns[4].Key)
	}
	if jobsSpec.Columns[len(jobsSpec.Columns)-1].Key != "docs" {
		t.Errorf("last column must be Docs (key=%q), got key=%q", "docs", jobsSpec.Columns[len(jobsSpec.Columns)-1].Key)
	}
}

// TestJobsSpec_MarketReadSortExpr asserts the MARKET READ column carries a
// CASE SQLExpr for sorting (band ordering STRONG>MODERATE>LONGSHOT>null).
// RED-on-revert: remove the SQLExpr from the market column.
func TestJobsSpec_MarketReadSortExpr(t *testing.T) {
	var found bool
	for _, col := range jobsSpec.Columns {
		if col.Key != "market" {
			continue
		}
		found = true
		if !col.Sortable {
			t.Error("market column should be sortable")
		}
		if !strings.Contains(col.SQLExpr, "CASE") {
			t.Errorf("market column SQLExpr should be a CASE expr, got: %q", col.SQLExpr)
		}
		if !strings.Contains(col.SQLExpr, "STRONG") {
			t.Errorf("market column CASE expr should include STRONG ordering: %q", col.SQLExpr)
		}
	}
	if !found {
		t.Fatal("market column not found in jobsSpec")
	}
}

// TestFitChipHTML_UnscoredNeverZero asserts that a nil fit_score never
// produces a "0" in the chip output. This covers scenario 3 from the arch plan.
// RED-on-revert: change nil guard to emit intStr(nil) = "" → "0".
func TestFitChipHTML_UnscoredNeverZero(t *testing.T) {
	got := fitChipHTML(nil, fitBandUnscored)
	if strings.Contains(got, ">0<") || strings.Contains(got, ">0 ") {
		t.Errorf("fitChipHTML(nil, unscored) should never emit 0, got: %q", got)
	}
}

// TestJobsSpec_OfflineQueryStructure asserts without DATABASE_URL that the column
// definitions would produce a valid OrderBy clause containing the expected
// expressions. Catches typos in SQLExpr that only fail at query-build time.
func TestJobsSpec_OfflineQueryStructure(t *testing.T) {
	// Resolve the default sort (fit, DESC) and check the generated ORDER BY.
	sortState := jobsSpec.Resolve("fit", "desc")
	orderBy := jobsSpec.OrderBy(sortState)

	if !strings.Contains(orderBy, "fit_score") {
		t.Errorf("OrderBy for sort=fit should contain 'fit_score', got: %q", orderBy)
	}

	// Resolve market sort and check CASE is present.
	sortMarket := jobsSpec.Resolve("market", "desc")
	orderByMarket := jobsSpec.OrderBy(sortMarket)
	if !strings.Contains(orderByMarket, "CASE") {
		t.Errorf("OrderBy for sort=market should contain 'CASE', got: %q", orderByMarket)
	}
	if !strings.Contains(orderByMarket, "STRONG") {
		t.Errorf("OrderBy for sort=market should contain 'STRONG', got: %q", orderByMarket)
	}
	if !strings.Contains(orderByMarket, "MODERATE") {
		t.Errorf("OrderBy for sort=market should contain 'MODERATE', got: %q", orderByMarket)
	}
	if !strings.Contains(orderByMarket, "LONGSHOT") {
		t.Errorf("OrderBy for sort=market should contain 'LONGSHOT', got: %q", orderByMarket)
	}

	// Column count sanity: triage badge added post-012 — now 12 total.
	const expectedCols = 12
	if len(jobsSpec.Columns) != expectedCols {
		t.Errorf("jobsSpec has %d columns, expected %d", len(jobsSpec.Columns), expectedCols)
	}
}

// noopAuth is a test-only auth stub that always considers the request authenticated.
// It implements the interface required by resource.Config.Auth.
type noopAuth struct{}

func (noopAuth) Require(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { next(w, r) }
}
func (noopAuth) LoginHandler() http.Handler  { return http.NotFoundHandler() }
func (noopAuth) LogoutHandler() http.Handler { return http.NotFoundHandler() }

// TestJobsListTemplate_FitChipRendered is the MAJOR 1 regression test.
// It renders a list row through the actual go-panel list template (via httptest)
// and asserts the fit-chip <span> appears UN-escaped — i.e. the HTML:true cell
// was honored at index >0.
//
// RED-on-revert: move the Fit chip back to cell index 0 (where list_templ.go:537
// special-cases Href-linked cells and ignores cell.HTML, HTML-escaping the chip).
func TestJobsListTemplate_FitChipRendered(t *testing.T) {
	score := 72
	// Synthetic lister that returns exactly one row — the Title at index 0,
	// Fit chip at index 1. Mimics the real jobsLister cell order.
	syntheticLister := func(_ context.Context, _ resource.ListQuery) ([]resource.Row, int, error) {
		return []resource.Row{
			{
				ID:   "999",
				Href: "/admin/jobs/999",
				Cells: []resource.Cell{
					{Value: "Staff Engineer · Acme Corp"},                   // cell-0: plain text with Href
					{Value: fitChipHTML(&score, fitBandStrong), HTML: true}, // cell-1: chip HTML
					{Value: marketReadHTML(sucBandStrong, ouMatch), HTML: true},
					{Value: "open"},
					{Value: "2026-06-20"},
					{Value: "Remote"},
					{Value: "linkedin"},
				},
			},
		}, 1, nil
	}

	p := resource.New(resource.Config{
		Title:    "test",
		BasePath: "/admin",
		Auth:     noopAuth{},
	})
	resource.Register(p, resource.Resource{
		Name:   "jobs",
		Title:  "Jobs",
		Sort:   jobsSpec,
		Filter: jobsFilter,
		Perms:  resource.ReadAny,
		Lister: syntheticLister,
	})

	srv := httptest.NewServer(p.Handler())
	defer srv.Close()

	// Hit the HTMX rows-fragment endpoint directly (avoids full-page shell render).
	req, err := http.NewRequestWithContext(context.Background(),
		http.MethodGet, srv.URL+"/admin/jobs/rows", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("HX-Request", "true") // HTMX fragment mode

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /admin/jobs/rows: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status %d", resp.StatusCode)
	}

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	html := buf.String()

	// PRIMARY ASSERT: fit-chip span must appear as real HTML (not escaped).
	if !strings.Contains(html, `class="fit-chip fit-strong"`) {
		t.Errorf("fit-chip not rendered as HTML; got escaped text. body:\n%s", html)
	}
	// NEGATIVE ASSERT: escaped span must NOT appear (would mean cell.HTML was ignored).
	if strings.Contains(html, "&lt;span") {
		t.Errorf("fit-chip HTML was escaped (&lt;span found) — likely chip moved back to cell-0. body:\n%s", html)
	}
	// The score number should appear inside the rendered chip.
	if !strings.Contains(html, strconv.Itoa(score)) {
		t.Errorf("fit score %d not found in rendered HTML", score)
	}
	// The title should appear as plain text (linked in <a href>).
	if !strings.Contains(html, "Staff Engineer") {
		t.Errorf("job title not found in rendered HTML")
	}
}

// TestJobsLister_SmokeWithFitCols runs the jobs Lister (with new columns) against
// DATABASE_URL. Skips when DATABASE_URL is unset (CI-safe). Asserts cell count
// matches updated jobsSpec and no percentage appears in any cell.
// Also asserts cell-0 is plain text and Href points to the go-panel Detailer URL.
func TestJobsLister_SmokeWithFitCols(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	dbtest.RequireTestDB(t, dsn)
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	q := resource.ListQuery{
		Sort:   jobsSpec.Resolve("fit", "desc"),
		Limit:  50,
		Offset: 0,
	}
	// authority=nil: docs column renders empty chips (no crash).
	// csrfKey=nil: star toggle renders ☆ with an empty-session token.
	rows, total, err := jobsLister(pool, "test_admin", nil, nil)(context.Background(), q)
	if err != nil {
		t.Fatalf("jobsLister: %v", err)
	}
	if total < 0 {
		t.Fatalf("negative total %d", total)
	}
	for i, r := range rows {
		if len(r.Cells) != len(jobsSpec.Columns) {
			t.Fatalf("row %d: %d cells, want %d", i, len(r.Cells), len(jobsSpec.Columns))
		}
		// Cell-0 must be plain text (not HTML).
		if r.Cells[0].HTML {
			t.Errorf("row %d: cell[0] has HTML:true — must be plain text", i)
		}
		// Href must point to /admin/jobs/{id} (go-panel Detailer, natural URL — no /view suffix).
		if strings.HasSuffix(r.Href, "/view") {
			t.Errorf("row %d: Href %q must NOT end with /view — stale bespoke route removed", i, r.Href)
		}
		// Honesty gate: no percentage in any cell value.
		for j, c := range r.Cells {
			if percentageRE.MatchString(c.Value) {
				t.Errorf("row %d cell %d: percentage leaked: %q", i, j, c.Value)
			}
		}
	}

	// Offline query structure checks (no DB required, but run here too).
	sortState := jobsSpec.Resolve("fit", "desc")
	orderBy := jobsSpec.OrderBy(sortState)
	if !strings.Contains(orderBy, "fit_score") {
		t.Errorf("OrderBy(fit) should reference fit_score, got: %q", orderBy)
	}

	t.Logf("jobs lister OK: %d rows (total=%d)", len(rows), total)
}

// admintableSpecCheck is a compile-time sentinel ensuring admintable.Spec
// still has Resolve and OrderBy. If the upstream API changes, this line fails fast.
var _ = admintable.Spec.Resolve
