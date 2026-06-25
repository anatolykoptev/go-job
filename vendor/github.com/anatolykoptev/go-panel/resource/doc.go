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
// Phase 1+ will add Detailer and Phase 2+ will add Writer. These are intentionally
// absent in foundations to keep the kit minimal and prove it on read-only entities first.
package resource
