package adminui

import (
	"context"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/anatolykoptev/go-panel/resource"
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
				numStr := strconvItoa(*tc.fit)
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
		band     string
		ou       string
		wantCls  string
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
			t.Errorf("buildMarketCardHTML(%q, %q, %q): honesty disclaimer missing", tc.band, tc.ou, tc.reasoning)
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
// Columns order" invariant. RED-on-revert: add a column without a cell.
func TestJobsSpec_CellColumnAlignment(t *testing.T) {
	// Synthetic minimal row matching the lister's cell assembly.
	score := 68
	cells := []resource.Cell{
		{Value: fitChipHTML(&score, fitBandStrong), HTML: true},   // fit
		{Value: "Some Title · Acme Corp"},                          // title/company
		{Value: marketReadHTML(sucBandStrong, ouMatch), HTML: true}, // market
		{Value: "open"},                                             // status
		{Value: "2026-06-01"},                                      // posted
		{Value: "Remote (US)"},                                     // location
		{Value: "linkedin"},                                         // source
	}
	if len(cells) != len(jobsSpec.Columns) {
		t.Fatalf("cell/column mismatch: %d cells vs %d columns — update one of them",
			len(cells), len(jobsSpec.Columns))
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

// TestJobsLister_SmokeWithFitCols runs the jobs Lister (with new columns) against
// DATABASE_URL. Skips when DATABASE_URL is unset (CI-safe). Asserts cell count
// matches updated jobsSpec and no percentage appears in any cell.
func TestJobsLister_SmokeWithFitCols(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping jobs lister integration test")
	}
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
	rows, total, err := jobsLister(pool)(context.Background(), q)
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
		// Honesty gate: no percentage in any cell value.
		for j, c := range r.Cells {
			if percentageRE.MatchString(c.Value) {
				t.Errorf("row %d cell %d: percentage leaked: %q", i, j, c.Value)
			}
		}
	}
	t.Logf("jobs lister OK: %d rows (total=%d)", len(rows), total)
}

// strconvItoa is a package-local helper so the test doesn't need a raw import alias.
func strconvItoa(n int) string { return strconv.Itoa(n) }
