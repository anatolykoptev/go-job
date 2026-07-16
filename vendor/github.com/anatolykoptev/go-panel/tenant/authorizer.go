package tenant

import "context"

// Authorizer decides whether the current session may access t.
//
// Authorizer is orthogonal to Resolver: Resolver answers "which tenant does
// this request name?" (derived from the URL/host, never trusted on its own);
// Authorizer answers "may this session touch that tenant's data?" (the actual
// access-control decision). A request always resolves to SOME Tenant — it is
// Authorizer's job to refuse the ones the session isn't entitled to.
//
// resource.Panel's guard is the single funnel: it composes an Authorizer
// check into every list/detail/rows-fragment/new/edit/save route for every
// registered Resource. A future I/O-backed implementation (e.g. a per-account
// allowed-cities lookup against the accounts store) MUST cache its decision
// for the life of the request — a naive implementation that queries a store
// on every call turns one inbound request into N round-trips, one per guarded
// route composing the check.
type Authorizer interface {
	// Authorized reports whether the session on ctx may access t.
	//
	// Callers MUST treat a non-nil error as DENY, identically to a false
	// result — never interpret an error as "undetermined, allow". This makes
	// a transient store failure fail closed rather than fail open.
	Authorized(ctx context.Context, t Tenant) (bool, error)
}

// IsGlobal reports whether t is the hard-coded global default tenant.
// Compares CitySlug only — the field Tenant's own doc comment documents as
// the sole load-bearing one; CountryCode is informational.
func IsGlobal(t Tenant) bool {
	return t.CitySlug == global.CitySlug
}

// GlobalOnlyAuthorizer allows the global tenant and denies every non-global
// tenant. It is the fail-closed default (resource.Config.TenantAuthorizer
// nil): every route in a single-tenant deployment resolves to the global
// tenant today, so this reproduces current reachable behaviour exactly —
// while refusing to silently allow a second tenant the moment one becomes
// resolvable (e.g. via /admin/tenant/msk/... once a PathResolver-backed
// Panel wires tenant resolution at Handler()).
//
// A real multi-tenant deployment must configure an explicit Authorizer (for
// example an account-scoped allowed-cities check) rather than rely on this
// default remaining permissive for a second tenant — it deliberately never
// will.
type GlobalOnlyAuthorizer struct{}

// Authorized implements Authorizer. Never returns a non-nil error — the
// decision is a pure comparison against the hard-coded global tenant.
func (GlobalOnlyAuthorizer) Authorized(_ context.Context, t Tenant) (bool, error) {
	return IsGlobal(t), nil
}
