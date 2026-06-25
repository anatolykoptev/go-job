// Package tenant provides the city-slug-as-scope seam for go-panel.
//
// Architecture: shared-schema + scope-column multitenancy. One PG instance;
// city_slug column on data tables. Matches ARCHITECTURE.md canon
// ("Single instance + city slug", per-tenant-DB rejected as over-engineered).
//
// Tenant is carried in context.Context from request entry. Every handler reads
// tenant.From(ctx). ScopeClause builds the safe WHERE fragment for scoped resources.
//
// Resolver interface abstracts tenant resolution strategy:
//   - PathResolver: /admin/tenant/{slug}/... → slug (matches go-nerv pattern)
//   - SubdomainResolver: spb.piter.now → "spb"
//
// TenantStore (deferred): hard-coded single SPb/RU tenant in foundations.
// DB-backed store is Phase 5 (post-PMF, multi-city).
package tenant
