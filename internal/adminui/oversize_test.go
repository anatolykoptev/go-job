package adminui

import (
	"strings"
	"testing"
)

// TestOversizeQuerySQL_ExcludesPayloadAndSample asserts that the SELECT query
// string used by the oversize resource never includes "payload" or "sample".
// This is a safety guard: both columns are potentially huge JSONB and must
// never be fetched in the list view.
func TestOversizeQuerySQL_ExcludesPayloadAndSample(t *testing.T) {
	q := oversizeQuerySQL("TRUE", "created_at DESC", 1, 2)

	if strings.Contains(q, "payload") {
		t.Errorf("oversize SELECT contains 'payload' — must be excluded: %s", q)
	}
	if strings.Contains(q, "sample") {
		t.Errorf("oversize SELECT contains 'sample' — must be excluded: %s", q)
	}
}

// TestOversizeSelectCols_ExcludesPayloadAndSample mirrors the test above but
// checks the const directly, guarding against future edits to oversizeSelectCols.
func TestOversizeSelectCols_ExcludesPayloadAndSample(t *testing.T) {
	for _, forbidden := range []string{"payload", "sample"} {
		if strings.Contains(oversizeSelectCols, forbidden) {
			t.Errorf("oversizeSelectCols contains %q — must be excluded: %s", forbidden, oversizeSelectCols)
		}
	}
}

// TestOversizeSpec_ColumnCount asserts spec has exactly 5 columns (tool, items,
// size, hash, created) — guards against accidental JSONB columns being added.
func TestOversizeSpec_ColumnCount(t *testing.T) {
	const want = 5
	if got := len(oversizeSpec.Columns); got != want {
		t.Errorf("oversizeSpec: %d columns, want %d", got, want)
	}
}
