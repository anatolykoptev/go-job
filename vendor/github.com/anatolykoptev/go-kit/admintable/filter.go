package admintable

import (
	"fmt"
	"net/url"
	"strings"
)

// Match selects the SQL predicate shape for a [Filter].
type Match int

const (
	// Eq emits an exact-equality predicate: "col = $N".
	// The bind arg is the string returned by [url.Values.Get](key).
	Eq Match = iota

	// AnyOf emits a set-membership predicate: "col = ANY($N::text[])".
	// The bind arg is a []string of all values for the key (url.Values[key]).
	//
	// pgx encodes a Go []string as a Postgres text[], which is what the
	// ::text[] cast expects.  Do not use AnyOf with drivers that do not
	// handle []string → text[] encoding.
	//
	// Empty-string elements inside the slice are NOT stripped — they become
	// bound array elements (harmless: they just won't match a row). Only an
	// entirely-empty slice (or one where Allowed rejects every value) skips
	// the filter.
	AnyOf

	// ILike emits a case-insensitive substring predicate across one or more
	// author-declared columns, OR'd together, all referencing a SINGLE bind
	// parameter:
	//
	//	(name ILIKE $3 ESCAPE '\' OR notes ILIKE $3 ESCAPE '\')
	//
	// The bound value is derived from vals.Get(key): the raw term is
	// LIKE-escaped (\ → \\, % → \%, _ → \_) and then wrapped as %term% so
	// that ILIKE performs a substring search.  The ESCAPE '\' clause tells
	// Postgres to honor the escaping, so a user searching for "50%" matches
	// the literal string "50%" and not every row.
	//
	// An empty term (or absent key) skips the filter — no index consumed.
	//
	// ILike consumes EXACTLY ONE arg index even when multiple columns are
	// declared, because Postgres allows a bind placeholder to be referenced
	// multiple times in a single query (e.g. $3 appears twice in the example
	// above, but only one bind value is passed).
	//
	// Column declaration: use [Filter.SQLExprs] (plural) — the slice must
	// contain at least one author-constant column expression.  [Filter.SQLExpr]
	// (singular) is unused for ILike and must be left empty; [FilterSpec.Valid]
	// enforces this.  [Filter.Allowed] must also be empty for ILike —
	// ILike is a free-text search with no closed value set, and Valid returns
	// an error if Allowed is non-empty.
	ILike
)

// Filter is a single vetted WHERE condition.
//
// SQLExpr / SQLExprs are the ONLY column-side expressions that ever reach SQL
// — they are AUTHOR-DECLARED compile-time constants.  URL parameter values go
// exclusively into bind args ($N); they are NEVER interpolated into the SQL
// string.
//
// IMPORTANT: SQLExpr and SQLExprs must be author-supplied constants, never
// derived from request data.  The security guarantee of the package depends
// on it.
//
// Field usage by Match type:
//
//   - [Eq]:    SQLExpr (non-empty), SQLExprs (unused / must be nil).
//   - [AnyOf]: SQLExpr (non-empty), SQLExprs (unused / must be nil).
//   - [ILike]: SQLExprs (≥1 element), SQLExpr (unused / must be empty).
//
// [FilterSpec.Valid] enforces these constraints at startup.
type Filter struct {
	// Key is the request-parameter name, e.g. "status" or "q".
	// A URL parameter whose key is not declared in the [FilterSpec] is silently ignored.
	Key string

	// SQLExpr is the author-constant column expression used in the WHERE predicate
	// for [Eq] and [AnyOf] filters, e.g. "subscription_status" or "u.plan_id".
	// Must be empty for [ILike] filters (use SQLExprs instead).
	// Never derive this from user input.
	SQLExpr string

	// SQLExprs is the author-constant slice of column expressions for [ILike]
	// filters.  Each element is included in the OR'd ILIKE predicate, e.g.
	// []string{"name", "u.notes"}.  Must be nil/empty for [Eq] and [AnyOf]
	// filters (use SQLExpr instead).
	// Never derive any element from user input.
	SQLExprs []string

	// Match selects the predicate shape: [Eq], [AnyOf], or [ILike].
	Match Match

	// Allowed is an optional closed set of accepted values for [Eq] and [AnyOf].
	// When non-empty, a request value not present in Allowed is treated as if
	// the parameter were absent (the filter is skipped — NOT an error).
	// This is the safe-degrade convention: unknown value → no filter applied.
	// When Allowed is empty every non-empty value is accepted.
	//
	// Must be nil/empty for [ILike] filters — ILike is a free-text search;
	// [FilterSpec.Valid] returns an error if Allowed is set on an ILike filter.
	Allowed []string
}

// FilterSpec is the declarative WHERE-filter contract for one admin list page.
//
// It mirrors [Spec] for sort: each [Filter] declares a single SQL condition;
// [FilterSpec.Valid] validates at startup; [FilterSpec.Where] resolves URL
// values into a safe, parameterised WHERE fragment at request time.
//
// Callers should invoke [FilterSpec.Valid] once at package-init or startup to
// detect misconfigured specs at boot rather than at query time.
type FilterSpec struct {
	Filters []Filter
}

// Valid reports whether the FilterSpec is correctly configured. It returns an
// error if:
//   - any two Filters share the same Key (duplicate keys cause ambiguous resolution),
//   - a Filter has a Match value outside the declared set ([Eq], [AnyOf], [ILike]),
//   - an [Eq] or [AnyOf] Filter has an empty SQLExpr (Where would emit a broken predicate),
//   - an [Eq] or [AnyOf] Filter has a non-nil SQLExprs (field reserved for ILike),
//   - an [ILike] Filter has zero SQLExprs (at least one column required),
//   - an [ILike] Filter has a non-empty SQLExpr (field unused for ILike),
//   - an [ILike] Filter has a non-empty Allowed (ILike is free-text; Allowed is meaningless).
//
// Intended to be called once at startup (e.g. in an init() function or a
// package-level var) to catch programmer errors early.
func (fs FilterSpec) Valid() error {
	seen := make(map[string]bool, len(fs.Filters))
	for i, f := range fs.Filters {
		if seen[f.Key] {
			return fmt.Errorf("admintable.FilterSpec: duplicate Filter Key %q", f.Key)
		}
		seen[f.Key] = true

		switch f.Match {
		case Eq, AnyOf:
			if f.SQLExpr == "" {
				return fmt.Errorf("admintable.FilterSpec: Filter[%d] Key %q has empty SQLExpr", i, f.Key)
			}
			if len(f.SQLExprs) > 0 {
				return fmt.Errorf("admintable.FilterSpec: Filter[%d] Key %q (Eq/AnyOf) must not set SQLExprs; use SQLExpr instead", i, f.Key)
			}
		case ILike:
			if len(f.SQLExprs) == 0 {
				return fmt.Errorf("admintable.FilterSpec: Filter[%d] Key %q (ILike) requires at least one SQLExprs element", i, f.Key)
			}
			if f.SQLExpr != "" {
				return fmt.Errorf("admintable.FilterSpec: Filter[%d] Key %q (ILike) must not set SQLExpr; use SQLExprs instead", i, f.Key)
			}
			if len(f.Allowed) > 0 {
				return fmt.Errorf("admintable.FilterSpec: Filter[%d] Key %q (ILike) must not set Allowed; ILike is a free-text search with no closed value set", i, f.Key)
			}
		default:
			return fmt.Errorf("admintable.FilterSpec: Filter[%d] Key %q has unknown Match value %d", i, f.Key, f.Match)
		}
	}
	return nil
}

// Where builds the AND-joined WHERE conditions from the given URL values.
//
// startArg is the next available bind-parameter index (e.g. 1 for a query with
// no prior args, or 3 if $1 and $2 are already used for pagination).
// Placeholders ($N) are numbered sequentially from startArg, in Filter
// declaration order, ONLY for active filters — skipped filters consume no index.
// args aligns 1:1 with the placeholders emitted in conds.
//
// A filter is skipped when:
//   - [Eq]: vals.Get(key) is empty.
//   - [AnyOf]: vals[key] is empty or nil.
//   - [ILike]: vals.Get(key) is empty.
//   - The resolved value(s) are not in Filter.Allowed (when Allowed is non-empty; not applicable to ILike).
//
// Returns ("", nil) when no filters are active.
// The returned conds string contains ONLY [Filter.SQLExpr] / [Filter.SQLExprs]
// values (author constants), the literal operators "= $N" / "= ANY($N::text[])"
// / "ILIKE $N ESCAPE '\'", and the literal conjunctives " AND " / " OR ".
// No URL value bytes ever appear in conds.
//
// [ILike] consumes exactly ONE arg index even when multiple columns are
// declared — Postgres allows a bind placeholder to be referenced multiple
// times in a single query.  The bound value is the LIKE-escaped term wrapped
// as %term% (see [Filter.SQLExprs] and the [ILike] constant for details).
//
// The returned conds does NOT include a leading WHERE or AND keyword — the
// caller is responsible for composing those:
//
//	conds, args := filterSpec.Where(r.URL.Query(), 1)
//	if conds != "" {
//	    query += " WHERE " + conds
//	}
//
//	// When combining with existing WHERE conditions:
//	conds, args := filterSpec.Where(r.URL.Query(), 3) // $1,$2 already used
//	if conds != "" {
//	    query += " AND " + conds
//	}
//
// Callers that pass conds to fmt.Sprintf should annotate:
//
//	//nolint:gosec // only FilterSpec-owned SQLExpr/SQLExprs + literal operators + $N
//	// placeholders reach SQL; URL values are bind args, never interpolated.
func (fs FilterSpec) Where(vals url.Values, startArg int) (conds string, args []any) {
	n := startArg
	var parts []string

	for _, f := range fs.Filters {
		switch f.Match {
		case Eq:
			v := vals.Get(f.Key)
			if v == "" {
				continue
			}
			if !f.allowed(v) {
				continue
			}
			//nolint:gosec // only author-declared SQLExpr + literal "= $N" reach SQL; v is a bind arg.
			parts = append(parts, fmt.Sprintf("%s = $%d", f.SQLExpr, n))
			args = append(args, v)
			n++

		case AnyOf:
			vs := vals[f.Key]
			if len(vs) == 0 {
				continue
			}
			filtered := f.allowedSlice(vs)
			if len(filtered) == 0 {
				continue
			}
			//nolint:gosec // only author-declared SQLExpr + literal "= ANY($N::text[])" reach SQL; filtered is a bind arg.
			parts = append(parts, fmt.Sprintf("%s = ANY($%d::text[])", f.SQLExpr, n))
			args = append(args, filtered)
			n++

		case ILike:
			v := vals.Get(f.Key)
			if v == "" {
				continue
			}
			//nolint:gosec // only author-declared SQLExprs elements + literal ILIKE operators reach SQL; bound is a bind arg.
			parts = append(parts, f.ilikePart(n))
			args = append(args, "%"+escapeLikeTerm(v)+"%")
			n++ // ILike consumes exactly one index regardless of column count.
		}
	}

	if len(parts) == 0 {
		return "", nil
	}
	return strings.Join(parts, " AND "), args
}

// ilikePart builds the ILIKE cond fragment for this filter at bind-parameter
// index n.  It is called only when f.Match == ILike and the search term is
// non-empty; f.SQLExprs is guaranteed non-empty by [FilterSpec.Valid].
//
// Single column → bare predicate (no parens):
//
//	"name ILIKE $3 ESCAPE '\'"
//
// Multiple columns → OR'd predicates in parens, all sharing $N:
//
//	"(name ILIKE $3 ESCAPE '\' OR notes ILIKE $3 ESCAPE '\')"
//
// Only author-declared SQLExprs elements and literal operator bytes reach the
// returned string; the search term itself is in the caller's bind arg.
func (f Filter) ilikePart(n int) string {
	clauses := make([]string, len(f.SQLExprs))
	for j, col := range f.SQLExprs {
		clauses[j] = fmt.Sprintf("%s ILIKE $%d ESCAPE '\\'", col, n)
	}
	if len(clauses) == 1 {
		return clauses[0]
	}
	return "(" + strings.Join(clauses, " OR ") + ")"
}

// escapeLikeTerm escapes the three LIKE metacharacters that Postgres interprets
// when ESCAPE '\' is in effect:
//
//	\  →  \\   (must be first to avoid double-escaping)
//	%  →  \%
//	_  →  \_
//
// The caller wraps the result as %term% to perform a substring (contains) search.
// This function is only called with URL parameter values — it never touches
// author-declared SQL expressions.
func escapeLikeTerm(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

// allowed reports whether v is an acceptable value for f.
// When f.Allowed is empty every non-empty string is accepted.
func (f Filter) allowed(v string) bool {
	if len(f.Allowed) == 0 {
		return true
	}
	for _, a := range f.Allowed {
		if a == v {
			return true
		}
	}
	return false
}

// allowedSlice returns the subset of vs whose elements are in f.Allowed.
// When f.Allowed is empty every element passes.
func (f Filter) allowedSlice(vs []string) []string {
	if len(f.Allowed) == 0 {
		return vs
	}
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		if f.allowed(v) {
			out = append(out, v)
		}
	}
	return out
}
