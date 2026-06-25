package admintable

import (
	"errors"
	"fmt"
	"strings"
)

// Column is a vetted, declarative column definition. SQLExpr is the ONLY thing
// ever concatenated into ORDER BY — author-supplied, never from the URL.
//
// IMPORTANT: SQLExpr and TieBreakSQLExpr are TRUSTED author inputs
// (compile-time constants). They are NOT validated at runtime. Never derive
// either field from user or request data.
//
// When NullsLast is true, [Spec.OrderBy] appends the literal " NULLS LAST" AFTER
// the direction word (e.g. "expr ASC NULLS LAST"). This is the only valid
// Postgres ORDER BY position for NULLS FIRST/LAST — it must follow the direction
// keyword. Use NullsLast for nullable date/time columns where NULL rows should
// sort to the bottom regardless of sort direction.
//
// When TieBreakSQLExpr is non-empty, [Spec.OrderBy] appends ", <TieBreakSQLExpr>"
// after the primary clause (including any NULLS LAST). The tie-break expression
// is emitted verbatim — it must already include its own direction keyword
// (e.g. "last_seen_at DESC"). Use this for columns where ties are likely
// (e.g. integer fit_score with a small range) to ensure stable pagination.
// Same trust model as SQLExpr: only author-declared compile-time constants.
type Column struct {
	Key             string // stable URL token carried by ?sort=, e.g. "name", "updated"
	Label           string // header text, e.g. "Name / Firm"
	Sortable        bool
	SQLExpr         string // vetted ORDER BY expression, e.g. "u.name", "u.updated_at". Compile-time constant.
	NullsLast       bool   // when true, OrderBy emits "<expr> <DIR> NULLS LAST" (valid Postgres ORDER BY syntax)
	TieBreakSQLExpr string // optional secondary sort term emitted verbatim (author-constant). E.g. "last_seen_at DESC".
	Width           string // optional inline width hint, e.g. "24%"
	Align           string // optional alignment hint: "", "center", or "right"
}

// Dir is the sort direction.
type Dir string

const (
	// Asc is ascending sort order.
	Asc Dir = "asc"
	// Desc is descending sort order.
	Desc Dir = "desc"
)

// Spec is the declarative contract for one admin table.
//
// Callers should invoke [Spec.Valid] once at package-init or startup to detect
// misconfigured Specs at boot rather than at query time.
type Spec struct {
	Columns    []Column
	DefaultKey string // must match a Sortable Column.Key
	DefaultDir Dir
}

// State is the resolved, safe sort selection (output of [Spec.Resolve],
// input to SQL generation and header rendering).
type State struct {
	Key string // a validated Sortable column key guaranteed present in Spec
	Dir Dir    // guaranteed Asc or Desc
}

// Valid reports whether the Spec is correctly configured. It returns an error if:
//   - there are zero Sortable columns,
//   - DefaultKey does not name a Sortable column,
//   - a Sortable column has an empty SQLExpr (OrderBy would emit a bare " ASC"),
//   - any two columns share the same Key (duplicate keys cause ambiguous resolution).
//
// Intended to be called once at startup (e.g. in an init() function or a
// package-level var) to catch programmer errors early.
func (sp Spec) Valid() error {
	// Check for duplicate keys.
	seen := make(map[string]bool, len(sp.Columns))
	for _, col := range sp.Columns {
		if seen[col.Key] {
			return fmt.Errorf("admintable.Spec: duplicate column Key %q", col.Key)
		}
		seen[col.Key] = true
	}

	// Check for at least one sortable column and a valid DefaultKey.
	hasSortable := false
	defaultKeyIsSortable := false
	for _, col := range sp.Columns {
		if col.Sortable {
			if col.SQLExpr == "" {
				return fmt.Errorf("admintable.Spec: Sortable column %q has empty SQLExpr", col.Key)
			}
			hasSortable = true
			if col.Key == sp.DefaultKey {
				defaultKeyIsSortable = true
			}
		}
	}
	if !hasSortable {
		return errors.New("admintable.Spec: no Sortable columns defined")
	}
	if !defaultKeyIsSortable {
		return fmt.Errorf("admintable.Spec: DefaultKey %q does not name a Sortable column", sp.DefaultKey)
	}

	return nil
}

// Resolve returns the safe, validated sort State for the given URL parameters.
// It starts from {DefaultKey, DefaultDir} and only overrides each field when
// the input passes exact equality against the Spec's closed sets:
//   - Key is only accepted if it equals some Column.Key where Column.Sortable is true.
//   - Dir is only accepted if strings.ToLower(strings.TrimSpace(dir)) is exactly "asc" or "desc".
//
// Raw URL parameters are never stored beyond these equality checks.
//
// The returned State always has Dir ∈ {Asc, Desc}: if DefaultDir is unset or
// invalid, it normalizes to Asc.
func (sp Spec) Resolve(sortKey, dir string) State {
	// Normalize DefaultDir — if it is neither Asc nor Desc, use Asc.
	defaultDir := sp.DefaultDir
	if defaultDir != Asc && defaultDir != Desc {
		defaultDir = Asc
	}
	st := State{Key: sp.DefaultKey, Dir: defaultDir}

	// Validate key: exact equality against sortable column keys only.
	for _, col := range sp.Columns {
		if col.Sortable && col.Key == sortKey {
			st.Key = col.Key
			break
		}
	}

	// Validate dir: case-insensitive, trimmed, exact match against "asc" or "desc".
	switch strings.ToLower(strings.TrimSpace(dir)) {
	case "asc":
		st.Dir = Asc
	case "desc":
		st.Dir = Desc
		// any other value: st.Dir stays at defaultDir (already set above)
	}

	return st
}

// OrderBy returns the ORDER BY fragment for st. The returned string is built
// entirely from the author-declared Column.SQLExpr, the literal direction words
// "ASC" or "DESC", and (when Column.NullsLast is set) the literal suffix
// " NULLS LAST" — no URL parameter bytes are present in the output.
//
// The NULLS LAST suffix is emitted AFTER the direction keyword, which is the
// only valid position in Postgres ORDER BY syntax:
//
//	"<expr> ASC NULLS LAST"   -- correct
//	"<expr> NULLS LAST ASC"   -- SQLSTATE 42601 syntax error
//
// Because st must have been produced by [Spec.Resolve], st.Key is guaranteed to
// match a Sortable column in the Spec. If somehow no match is found (defensive
// branch), the default column expression is used instead.
//
// Callers passing this output to fmt.Sprintf should annotate:
//
//	//nolint:gosec // only Spec-owned SQLExpr + literal "ASC"/"DESC" + optional
//	// literal " NULLS LAST" reach SQL; URL params are equality-matched against
//	// a closed set, never interpolated.
func (sp Spec) OrderBy(st State) string {
	expr := sp.defaultExpr()
	nullsLast := false
	tieBreak := ""
	for _, col := range sp.Columns {
		if col.Sortable && col.Key == st.Key {
			expr = col.SQLExpr
			nullsLast = col.NullsLast
			tieBreak = col.TieBreakSQLExpr
			break
		}
	}
	dirStr := " ASC"
	if st.Dir == Desc {
		dirStr = " DESC"
	}
	result := expr + dirStr
	if nullsLast {
		// Direction BEFORE NULLS LAST — the only valid Postgres ORDER BY syntax.
		result = expr + dirStr + " NULLS LAST"
	}
	if tieBreak != "" {
		// TieBreakSQLExpr is a closed-set author constant (same trust model as SQLExpr).
		// It already embeds its own direction keyword — appended verbatim.
		//nolint:gosec // only author-declared compile-time TieBreakSQLExpr constant reaches SQL; never URL input.
		result += ", " + tieBreak
	}
	return result
}

// defaultExpr returns the SQLExpr of the first Sortable column whose Key
// matches DefaultKey. If DefaultKey matches no Sortable column (misconfigured
// Spec), it falls back to the first Sortable column's SQLExpr. If there are
// no Sortable columns at all, it returns the safe constant "1" (prevents a bare
// " ASC"/" DESC" fragment, though [Spec.Valid] will have caught this at startup).
//
// Used as the defensive fallback in [Spec.OrderBy] when st.Key has no Sortable match.
func (sp Spec) defaultExpr() string {
	var firstSortable string
	for _, col := range sp.Columns {
		if col.Sortable {
			if firstSortable == "" {
				firstSortable = col.SQLExpr
			}
			if col.Key == sp.DefaultKey {
				return col.SQLExpr
			}
		}
	}
	if firstSortable != "" {
		return firstSortable
	}
	// Truly no sortable columns — Valid() should have caught this at startup.
	// Return safe constant to prevent a broken fragment from reaching SQL.
	return "1"
}
