// Package tenant provides the city-slug-as-scope seam for go-panel.
//
// Architecture: shared-schema + scope-column multitenancy — one PG instance,
// city_slug column on data tables. This matches ARCHITECTURE.md canon:
// "Single instance + city slug" (KudaGo runs 17 cities this way).
// Anything heavier (per-tenant DB, per-tenant schema) is out of scope.
//
// Routing mutation: PathResolver.StripPrefix is the one function in this
// package that is NOT pure-read — it rewrites a request path, removing the
// /tenant/{slug} segment pair the marker guard requires, so the underlying
// mux pattern (e.g. "GET /admin/{name}") can match. Callers must invoke it
// exactly once, before mux dispatch (resource.Panel.Handler does this via its
// withTenantResolution wrap) — calling it later, or from more than one
// composition point, risks stripping a segment the mux has already routed
// on. StripPrefix itself never mutates a shared *http.Request/*url.URL; the
// caller owns cloning before assigning the rewritten path back.
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

// markerSlug locates the tenant slug in path: the segment at pr.Segment,
// honoured ONLY when the immediately preceding segment is the literal
// tenantPathMarker and the slug segment itself is non-empty (the documented
// /admin/tenant/{slug}/... shape).
//
// Resolve and StripPrefix both call this — it is the ONE split+guard
// expression behind both, so they share a single source of truth by
// construction rather than two independently written copies of the same
// condition kept in sync only by convention (+ the twin regression tests).
//
// Returns the located slug and its segment index when ok is true; when ok is
// false, slug/idx are zero-valued and the caller falls back to its own
// "no tenant" behaviour (Resolve: the global tenant; StripPrefix: the
// unchanged path).
func (pr PathResolver) markerSlug(path string) (slug string, idx int, ok bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if pr.Segment >= 1 && pr.Segment < len(parts) && parts[pr.Segment-1] == tenantPathMarker {
		if s := parts[pr.Segment]; s != "" {
			return s, pr.Segment, true
		}
	}
	return "", 0, false
}

// Resolve implements Resolver. The slug at Segment is honoured ONLY when the
// preceding segment is the literal "tenant" (the documented
// /admin/tenant/{slug}/... shape); any other URL falls back to the global
// default tenant.
func (pr PathResolver) Resolve(r *http.Request) Tenant {
	if slug, _, ok := pr.markerSlug(r.URL.Path); ok {
		return Tenant{
			CitySlug:    slug,
			CountryCode: global.CountryCode,
		}
	}
	return global
}

// StripPrefix removes the /tenant/{slug} segment pair from path, using
// markerSlug — the SAME guard Resolve calls — so resolution and stripping
// share one source of truth and cannot diverge by construction.
//
// Returns (path, false) unchanged — byte-for-byte, not merely equivalent —
// whenever markerSlug finds no marker, mirroring Resolve's own
// fallback-to-global guard exactly. Returns (rewritten, true) when the pair
// is removed.
//
// Pure: StripPrefix never mutates a shared *http.Request or *url.URL. The
// caller (resource.Panel's withTenantResolution) is responsible for cloning
// before assigning the returned path back onto a request — see the package
// doc's routing-mutation note.
func (pr PathResolver) StripPrefix(path string) (string, bool) {
	_, idx, ok := pr.markerSlug(path)
	if !ok {
		return path, false
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	rest := append(append([]string{}, parts[:idx-1]...), parts[idx+1:]...)
	return "/" + strings.Join(rest, "/"), true
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
