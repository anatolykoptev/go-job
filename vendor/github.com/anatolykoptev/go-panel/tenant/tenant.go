// Package tenant provides the city-slug-as-scope seam for go-panel.
//
// Architecture: shared-schema + scope-column multitenancy — one PG instance,
// city_slug column on data tables. This matches ARCHITECTURE.md canon:
// "Single instance + city slug" (KudaGo runs 17 cities this way).
// Anything heavier (per-tenant DB, per-tenant schema) is out of scope.
//
// Usage:
//
//	// At request entry (via Middleware):
//	tenant := tenant.From(r.Context())
//
//	// In a Resource Lister:
//	cond, arg := tenant.ScopeClause(res.Scope, t)  // "p.city_slug = $N"
package tenant

import (
	"context"
	"net/http"
	"strings"
)

// ctxKey is the unexported context key for Tenant values.
type ctxKey struct{}

// Tenant carries per-request city scope. Only CitySlug is load-bearing for
// query scoping; CountryCode is informational and available to templates for
// future branding use.
//
// Locale is deliberately NOT a Tenant field: i18n is orthogonal to tenancy and
// owned by the locale package (locale.Set / locale.From(ctx)). A tenant may be
// served in several locales, so locale lives on the request context, not here.
type Tenant struct {
	CitySlug    string // e.g. "spb", "msk" — the scope column value
	CountryCode string // e.g. "RU" — for branding (deferred)
}

// global is the hard-coded single SPb/RU tenant used until a second city exists.
// Replace with a DB-backed TenantStore in Phase 5.
var global = Tenant{
	CitySlug:    "spb",
	CountryCode: "RU",
}

// From retrieves the Tenant from ctx. Returns the global default when no tenant
// is set — safe to call without Middleware in single-tenant deployments.
func From(ctx context.Context) Tenant {
	if t, ok := ctx.Value(ctxKey{}).(Tenant); ok {
		return t
	}
	return global
}

// WithTenant stores t in ctx.
func WithTenant(ctx context.Context, t Tenant) context.Context {
	return context.WithValue(ctx, ctxKey{}, t)
}

// Scope declares how a Resource is scoped to a tenant.
// Column is a table-qualified column name, e.g. "p.city_slug".
// An empty Column means the resource is global (not scoped).
type Scope struct {
	Column string // e.g. "p.city_slug". Compile-time constant — never from URL.
}

// Global is a sentinel Scope indicating no tenant filtering.
var Global = Scope{}

// ScopeClause returns the WHERE fragment and bind arg for the given scope and
// tenant. Returns ("", nil) for a global scope (resource is not tenant-scoped).
//
// startArg is the positional arg index for the bind param (1-based for pgx).
//
// Callers passing the returned cond to fmt.Sprintf should annotate:
//
//	//nolint:gosec // only Scope-owned Column (author constant) + literal "= $N"; value is a bind arg.
func ScopeClause(s Scope, t Tenant, startArg int) (cond string, arg any) {
	if s.Column == "" {
		return "", nil
	}
	//nolint:gosec // only author-declared compile-time Column constant + literal placeholder; value is a bind arg.
	return s.Column + " = $" + itoa(startArg), t.CitySlug
}

// Resolver resolves a Tenant from an HTTP request.
type Resolver interface {
	Resolve(r *http.Request) Tenant
}

// PathResolver resolves a Tenant from a URL path segment:
//   - /admin/tenant/{slug}/... → slug
//
// Falls back to the global default when no {tenant} segment is present.
// Matches go-nerv's /admin/tenant/{tenant} route pattern.
type PathResolver struct {
	// Segment is the URL path segment index (0-based) containing the slug.
	// For /admin/tenant/{slug}/..., Segment is 2.
	Segment int
}

// tenantPathMarker is the literal path segment that must precede the slug.
// Without this guard the resolver grabs ANY segment at the configured index:
// /admin/rating_sponsorships/new resolved to city "new" and /{name}/{id}/edit
// to city "{id}", silently breaking every city-scoped query behind a form
// (empirically hit in go-grad on 2026-06-11).
const tenantPathMarker = "tenant"

// Resolve implements Resolver. The slug at Segment is honoured ONLY when the
// preceding segment is the literal "tenant" (the documented
// /admin/tenant/{slug}/... shape); any other URL falls back to the global
// default tenant.
func (pr PathResolver) Resolve(r *http.Request) Tenant {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if pr.Segment >= 1 && pr.Segment < len(parts) && parts[pr.Segment-1] == tenantPathMarker {
		if slug := parts[pr.Segment]; slug != "" {
			return Tenant{
				CitySlug:    slug,
				CountryCode: global.CountryCode,
			}
		}
	}
	return global
}

// SubdomainResolver resolves a Tenant from the Host header subdomain:
//   - spb.piter.now → "spb"
//   - piter.now → global default
type SubdomainResolver struct{}

// Resolve implements Resolver.
func (SubdomainResolver) Resolve(r *http.Request) Tenant {
	host := r.Host
	if idx := strings.Index(host, ":"); idx >= 0 {
		host = host[:idx]
	}
	// Require at least 3 dot-separated parts (subdomain.domain.tld) so that
	// bare "domain.tld" does not incorrectly treat "domain" as a city slug.
	parts := strings.Split(host, ".")
	if len(parts) >= 3 && parts[0] != "" {
		slug := parts[0]
		return Tenant{
			CitySlug:    slug,
			CountryCode: global.CountryCode,
		}
	}
	return global
}

// Middleware returns an http.Handler that resolves a Tenant from r and stores
// it in the request context. Use this at the admin router boundary.
func Middleware(resolver Resolver, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t := resolver.Resolve(r)
		next.ServeHTTP(w, r.WithContext(WithTenant(r.Context(), t)))
	})
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	const bufCap = 10 // enough for any int32 decimal representation
	buf := make([]byte, 0, bufCap)
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	return string(buf)
}
