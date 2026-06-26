// Package resource is the core abstraction of go-panel — the "Directus-as-a-library" layer.
//
// A consumer declares a Resource (table, Sort columns, Filter dimensions, tenant Scope,
// Lister closure) and calls Register to get a working admin list page: sort headers,
// filter bar, pagination, and sidebar nav entry — all generated from the declaration
// with zero hand-written table HTML.
//
// SQL-safety invariant: only admintable.Spec SQLExpr + FilterSpec SQLExpr values +
// literal operators reach SQL. URL bytes become bind args only.
// The Lister closure receives pre-composed WhereConds/WhereArgs — it must never
// build additional WHERE from raw URL params.
//
// Tenant-scope invariant: every non-Global resource gets city_slug WHERE injected
// unconditionally. The fitness test in resource_test.go asserts this.
//
// # Detailer (Show view)
//
// Set Resource.Detailer to enable a per-row detail page at
// GET {basePath}/{name}/{id}.  The closure signature is (ctx, r, id)
// where r is the *http.Request, enabling CSRF token minting or session reads
// for interactive RawHTML sections.  The closure returns []DetailSection — a
// schema-agnostic list of titled cards, each containing []DetailItem (label +
// value) or a RawHTML block (for consumer-built panels such as a two-column
// fit/gap card).  go-panel owns the chrome (shell.Layout, back-link, nav);
// the consumer owns the content.
//
//	resource.Register(panel, resource.Resource{
//	    Name:  "jobs",
//	    Title: "Jobs",
//	    Lister: jobsLister,
//	    Detailer: func(ctx context.Context, r *http.Request, id string) ([]resource.DetailSection, error) {
//	        // r is available for CSRF token minting or session reads in RawHTML sections.
//	        job, err := store.GetJob(ctx, id)
//	        if err != nil { return nil, err }
//	        return []resource.DetailSection{
//	            {Title: "Overview", Items: []resource.DetailItem{
//	                {Label: "Company", Value: job.Company},
//	                {Label: "Location", Value: job.Location},
//	            }},
//	        }, nil
//	    },
//	})
//
// XSS contract: DetailItem.Value is HTML-escaped by go-panel unless HTML=true.
// Set HTML=true only for values assembled from closed-enum constants (e.g. a
// chip HTML string built from a band map) — never for raw DB or user text.
// DetailSection.RawHTML is rendered verbatim via templ.Raw; the consumer is
// responsible for ensuring it is XSS-free before embedding.
//
// # Status chips (CSS)
//
// The theme in shell/styles.templ ships two reusable chip families for consumers
// that render scored/classified data:
//
//   - Fit axis:    .fit-chip + .fit-strong / .fit-moderate / .fit-weak / .fit-low /
//     .fit-reject / .fit-unscored  (green→red ramp, monospace)
//   - Market Read: .suc-chip + .suc-strong / .suc-moderate / .suc-longshot  (purple family)
//   - Over/under:  .ou-glyph + .ou-over / .ou-match / .ou-under  (inline glyphs)
//
// These are intentionally orthogonal to the existing .badge-* classes (which carry
// semantic system success/error meaning) — do not substitute one for the other.
//
// # Column Width / Align
//
// admintable.Column.Width and .Align are now wired through list.templ.
// Set Width (e.g. "7rem") to constrain a column and Align ("right", "center")
// to control cell text-align.  Both map to inline styles on <th> and <td>.
package resource
