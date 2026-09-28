// Package resource is the core abstraction of go-panel.
//
// A consumer declares a Resource once and calls Register to get a working
// admin list page: sort headers, filter bar, pagination, nav entry — all
// generated from the declaration with zero hand-written table HTML.
//
// SQL-safety invariant: only Spec-owned SQLExpr + FilterSpec-owned SQLExpr
// values + literal operators reach SQL. URL bytes become bind args only.
// The Resource.Lister closure receives pre-composed WhereConds/WhereArgs
// from FilterSpec — it must never build additional WHERE from raw URL params.
//
// Tenant-scope invariant: every non-Global resource gets the city_slug WHERE
// clause injected unconditionally. A resource with an empty Scope = global
// (no scope injection). The fitness test in this package asserts this.
//
// Usage:
//
//	var placesRes = resource.Resource{
//	    Name:  "places",
//	    Title: "Places",
//	    Sort:  admintable.Spec{...},
//	    Filter: admintable.FilterSpec{...},
//	    Scope: tenant.Scope{Column: "p.city_slug"},
//	    Lister: func(ctx context.Context, q resource.ListQuery) ([]resource.Row, int, error) {
//	        return store.ListPlaces(ctx, q)
//	    },
//	}
//	resource.Register(panel, placesRes)
package resource

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/anatolykoptev/go-kit/admintable"
	"github.com/anatolykoptev/go-panel/csrf"
	"github.com/anatolykoptev/go-panel/locale"
	"github.com/anatolykoptev/go-panel/render"
	"github.com/anatolykoptev/go-panel/shell"
	"github.com/anatolykoptev/go-panel/tenant"
)

const (
	defaultPageSize = 50
	maxPageSize     = 500
	// minCSRFKeyLen is the minimum acceptable length for CSRFKey.
	// Keys shorter than this are rejected at Register time (fail-closed).
	minCSRFKeyLen = 32
	// idNew is the reserved path segment used to represent a "create new record"
	// request. It is rejected with 404 on detail/edit routes (which require an
	// existing record) and treated as a create signal by the save handler.
	idNew = "new"
	// maxLogIDLen caps the length of an attacker-controlled id value emitted
	// in a structured log attribute. Without a cap, one request can write an
	// arbitrarily large log line (the id arrives from the URL path verbatim).
	maxLogIDLen = 256
	// tenantAuthzDefaultWarning is the stable, greppable message New logs
	// once at construction when Config.TenantAuthorizer is left nil
	// (defaulting to tenant.GlobalOnlyAuthorizer) — the only runtime signal
	// against a silent cross-tenant exposure once a second tenant becomes
	// resolvable. Keep this string stable: Loki/dozor alerting keys off it.
	tenantAuthzDefaultWarning = "panel: tenant authorization not configured, defaulting to GlobalOnlyAuthorizer"
)

// Cell is one table cell value.
type Cell struct {
	Value string
	HTML  bool // when true, Value is rendered as raw HTML (must be trusted/escaped by caller)
}

// Row is one table row in a list response.
type Row struct {
	ID    string
	Cells []Cell // ordered to match Sort.Columns
	Href  string // optional detail link
}

// DetailItem is one label-value row inside a DetailSection.
//
// Value is plain text and HTML-escaped by go-panel before rendering.
// Set HTML=true ONLY for values assembled from closed-enum constants by the
// consumer (e.g. a chip returned by a band-chip helper) — never for raw DB
// or user-supplied text, which must go through text escaping first.
type DetailItem struct {
	Label string
	Value string
	HTML  bool // when true, Value is rendered via templ.Raw — caller guarantees safety
}

// DetailColumn is one column header of a DetailTable.
type DetailColumn struct {
	Label string
	Align string // "", "center", or "right" — mirrors admintable.Column.Align
}

// DetailCell is one cell of a DetailTable row. Value is ALWAYS rendered as
// escaped text. Href, when non-empty, wraps it in an anchor.
//
// Href is passed to templ as a PLAIN STRING so templ's own URL sanitiser runs
// on it, rewriting a dangerous scheme to about:invalid#TemplFailedSanitizationURL.
// Never wrap it in templ.SafeURL: that type is not a sanitiser, it is the
// opt-OUT from the sanitiser, and a cell Href is consumer data built from a
// database row. Measured — with the SafeURL cast, javascript:alert(1) reached
// the rendered anchor verbatim.
//
// There is deliberately NO per-cell HTML flag: the whole point of DetailTable
// is to give tabular detail data a structured home so it stops reaching for the
// RawHTML hatch. A cell that needs a link uses Href; anything richer belongs on
// a dedicated component, not a third escape mode here.
type DetailCell struct {
	Value string
	Href  string
}

// DetailTable is a multi-column row list inside a DetailSection. It renders as
// a .crm-table--static (non-navigating) table with escaped text cells and
// optional per-cell anchors. Use it for the tabular data a detail page carries
// alongside its label/value summary — invoices, payments, per-page breakdowns.
type DetailTable struct {
	Columns []DetailColumn
	Rows    [][]DetailCell
}

// DetailSection is one logical card / group on the Detail page.
// A section has an optional title and any of: a list of Items, a Table, or a
// RawHTML block. RawHTML takes precedence: when non-empty it wins and NEITHER
// Items nor Table render. Otherwise Items render first (the existing
// label/value summary) and then the Table — a summary line above its table is
// the intended shape. RawHTML is for consumer-supplied pre-rendered HTML panels
// (e.g. a two-column fit-card); it must never contain raw DB/user text — escape
// before embedding.
type DetailSection struct {
	Title   string
	Items   []DetailItem
	Table   DetailTable
	RawHTML string // consumer-supplied HTML; must be safe (XSS-free) before use
}

// ListQuery is the safe, resolved query handed to the Lister closure.
// It contains only values safe to use in SQL: pre-composed WHERE fragment
// (from FilterSpec — only author SQLExpr + literal ops), bind args, resolved
// sort state, tenant, and paging.
//
// IMPORTANT: never build additional WHERE from the raw HTTP request inside
// a Lister. Use only WhereConds/WhereArgs + Tenant + Sort.
type ListQuery struct {
	Sort       admintable.State
	WhereConds string // from FilterSpec.Where — only author SQLExpr + literal ops
	WhereArgs  []any  // URL values as bind args; index from 1 = startArg passed to FilterSpec.Where
	Tenant     tenant.Tenant
	Limit      int
	Offset     int

	// View is the selected [Resource.Views] key, or "" when the resource
	// declares none. It is ALWAYS one of the declared keys: the framework
	// resolves the URL value against the closed set and falls back to the first
	// view, so a Lister may switch on it without validating.
	//
	// This exists because some resources have MODES that a WHERE clause cannot
	// express — most obviously an aggregate whose GROUP BY grain the operator
	// picks (per week / per month / per quarter). Before Views, such a page had
	// to be hand-built outside the framework, losing the table, the sorting,
	// the chips and the stylesheet with it. The rule above ("never build SQL
	// from the raw request") is what makes that tempting and Views is the
	// sanctioned way out: the key is declared by the author, validated by the
	// framework, and reaches the Lister as data.
	//
	// A view NEVER carries a SQL fragment. Map the key to author-owned SQL
	// inside the Lister, the same way a Filter maps to an author-owned SQLExpr.
	View string
}

// View is one named mode of a resource, rendered as a filter chip and passed to
// the Lister as [ListQuery.View]. The set is closed and author-declared: a URL
// value outside it never reaches the Lister.
type View struct {
	Key   string // URL value, e.g. "week". Must be non-empty and unique within the resource.
	Label string // chip label, e.g. "This week". Falls back to Key when empty.
}

// Resource is the declarative contract for one admin entity.
// Declare once, get list + (later) detail + edit + MCP for free.
//
// Compile-time invariant: Sort and Filter must be validated at startup via
// Register (panics on invalid Spec/FilterSpec). Never derive Sort.Columns
// SQLExpr or Filter.Filters SQLExpr from user input.
type Resource struct {
	Name   string // URL/nav slug, e.g. "places". Must be URL-safe.
	Title  string // human label, e.g. "Places"
	Icon   string // sidebar emoji/icon, e.g. "📍"
	Group  string // sidebar group label, e.g. "Content". Empty = no group.
	Sort   admintable.Spec
	Filter admintable.FilterSpec
	Scope  tenant.Scope // city_slug scope; empty = global

	// Views is an optional closed set of modes for this resource, rendered as
	// filter chips beside the other filters and delivered to the Lister as
	// [ListQuery.View]. The FIRST entry is the default. Empty (the common case)
	// means the resource has one mode and ListQuery.View is always "".
	//
	// Use it for a mode a WHERE clause cannot express — an aggregate's GROUP BY
	// grain is the motivating case. Do NOT use it for something Filter already
	// does: a view is not a filter, it changes what a row MEANS.
	//
	// Register panics on an empty or duplicate Key (fail-fast at startup).
	Views []View

	// ViewsLabel is the caption shown beside the view chips. Defaults to "view".
	ViewsLabel string

	// RequiredRole is the SOLE authorization lever for this resource, applied
	// uniformly to every route — read (list, detail) AND write (new/edit/save).
	// Only a session whose role equals RequiredRole (or the "owner" super-role)
	// may reach any of them; everyone else gets 403. Empty (default) = no role
	// gate: any authenticated operator may read and write, preserving the
	// foundational behaviour.
	//
	// A non-empty RequiredRole requires the configured authenticator to
	// implement RoleAuthenticator; Register panics at startup otherwise
	// (fail-closed). The same role drives nav-hiding via HasRole so the sidebar
	// does not surface a resource the operator cannot open.
	RequiredRole string

	// Lister fetches one page of rows. The kit hands it a safe ListQuery;
	// the app owns the row type + scan. go-panel never assumes a schema.
	Lister func(ctx context.Context, q ListQuery) (rows []Row, total int, err error)

	// TrashLister fetches this resource's DELETED rows for the panel-wide Trash
	// page, newest-deleted first. Nil (default) = this resource contributes
	// nothing to the trash and, if no resource sets it, no Trash page exists at
	// all.
	//
	// It is a second reader rather than a flag on Lister because the two answer
	// different questions and must not share a query: Lister reads whatever
	// live view the consumer built (in go-grad, live_lead), while TrashLister
	// deliberately reads the base table with the filter INVERTED. Folding both
	// into one closure behind a boolean is how a caller ends up passing the
	// wrong one — the mistake every ORM in the survey shipped at least once.
	//
	// Only Limit and Tenant of the ListQuery are populated; ordering is the
	// consumer's, because only it knows which column records the deletion.
	//
	// Applying q.Tenant is the CONSUMER'S OBLIGATION and is easy to miss here
	// specifically: Lister usually reads a view that already carries the tenant
	// predicate, while TrashLister reads the base table on purpose — so it drops
	// exactly the scoping the view was supplying. A TrashLister that ignores
	// q.Tenant shows one tenant's deleted rows to another.
	//
	// RequiredRole is the only authorization lever the Trash page honours. A
	// resource gated by anything else — a reverse-proxy path rule, middleware
	// outside the panel — is off-contract (see RequiredRole), and the Trash
	// aggregates its rows onto a page that gate never sees.
	// total is every deleted row, not just the returned page — the Trash page
	// prints it beside what it drew, so a cap is never mistaken for the whole
	// set.
	//
	// Register panics when TrashLister is set without Writer.Delete AND
	// Writer.Restore: a trash you cannot restore from is a dead end, and one
	// nothing can put rows into is furniture.
	TrashLister func(ctx context.Context, q ListQuery) (rows []Row, total int, err error)

	// Detailer enables a per-row Detail (Show) view at GET {basePath}/{name}/{id}.
	// Nil = no detail page (default — preserves existing behaviour).
	// When non-nil, Register mounts GET {basePath}/{name}/{id} and the template
	// renders the returned sections inside the standard shell.Layout.
	// id=="new" is rejected with 404 (symmetric with the edit route).
	// The closure must be safe to call concurrently (standard Go handler rules).
	// See DetailSection / DetailItem for the schema-agnostic shape.
	Detailer func(ctx context.Context, r *http.Request, id string) ([]DetailSection, error)

	// FetchRow fetches a single row by ID for an auto-generated detail page.
	// When Detailer is nil but FetchRow is non-nil, Register synthesizes a
	// Detailer that renders one DetailSection with a DetailItem per Sort.Columns
	// entry (Label from Column.Label, Value from the returned map keyed by
	// Column.Key). This lets resources without a hand-written Detailer still
	// serve /{name}/{id} so cross-link cells work (the cross-resource linking
	// 404 problem — see go-panel issue #100 / go-grad PR #266).
	// When both Detailer and FetchRow are nil, no detail route is mounted
	// (existing behaviour — preserves backward compatibility).
	// When both are non-nil, Detailer wins (FetchRow ignored).
	// The closure must be safe to call concurrently (standard Go handler rules).
	// Return ErrDetailNotFound to signal a 404 (same sentinel as Detailer).
	FetchRow func(ctx context.Context, id string) (map[string]string, error)

	// Writer enables create/edit forms. Nil = read-only (Phase 1 behaviour, default).
	// Its Load/Save/Delete/Restore closures may return ErrDetailNotFound for an
	// id that names no row; the framework answers 404 rather than 500.
	// When non-nil, CSRFKey must be set in Config (panic at Register if missing or < 32 bytes — fail-closed).
	Writer *Writer

	// Visible is a cosmetic predicate that controls nav-link visibility.
	// nil = always visible (zero-value safe, default behaviour).
	// When non-nil, the resource layer evaluates it before passing the nav list
	// to Layout; items whose Visible returns false are dropped from the sidebar.
	//
	// SECURITY: Visible is COSMETIC ONLY — it hides the nav link but does NOT
	// gate the route. An operator who knows the URL can still reach the resource.
	// Use RequiredRole to gate the route itself. Visible is the right tool for
	// feature flags, tenant-tier restrictions, or any cosmetic hide that does
	// NOT need to enforce access control.
	Visible func(ctx context.Context) bool

	// Badge is an optional closure returning a short live count or label displayed
	// as a pill next to the nav item (e.g. "12", "new"). Called once per page render —
	// wrap with shell.CachedBadge to avoid a per-render DB query (footgun: a raw
	// DB COUNT per render balloons admin-page latency under many resources).
	// nil = no badge (zero-value safe; existing callers are unaffected).
	Badge func(ctx context.Context) string

	// Relations declares BelongsTo cross-resource links for this resource's
	// list cells. Each Relation replaces a foreign-key cell with an XSS-safe
	// CrossLinkCell anchor pointing at the target resource's detail route.
	// Zero-value (nil) = no relations (backward compatible). Resolved by
	// resolveRelations, which is NOT yet wired into makeListHandler (Phase 3).
	// Register-time validation is self-contained: each Relation.ForeignKey
	// must match a Sort.Columns[].Key on THIS resource (ADR-6).
	Relations []Relation

	// SingleRow indicates a single-row resource (e.g. a profile, settings).
	// When true:
	//   - GET /{name} redirects to /{name}/{id}/edit for the first (only) row.
	//   - GET /{name}/new is not mounted (no create — the row already exists).
	//   - Lister is still required (used to find the row ID for the redirect).
	//   - Writer.Load and Writer.Save are called with that row's ID.
	// Requires Writer to be non-nil (Register panics otherwise).
	SingleRow bool
}

// Panel is the minimal composition root go-panel provides.
// It holds the mux, authenticator, tenant resolver, and the registered nav.
// Consumers create it via New() and call Handler() to get the http.Handler.
type Panel struct {
	// mux is the internal ServeMux. The index route is registered lazily, in
	// finalize() on the first Handler() call — so mux must never be exposed
	// except via Handler(); a future accessor handing out p.mux directly
	// (without routing through Handler()) would serve a 404 at the index.
	mux  *http.ServeMux
	auth baseAuthenticator
	// resolver resolves the per-request Tenant; read by withTenantResolution
	// in Handler() — the one place tenant resolution happens (see the tenant
	// package doc's routing-mutation note).
	resolver tenant.Resolver
	// tenantAuthz decides whether the resolved Tenant may be accessed by the
	// current session; composed into every guarded route via requireTenant.
	// Never nil after New() — defaults to tenant.GlobalOnlyAuthorizer{}
	// (fail-closed: allow the global tenant, deny every other one).
	tenantAuthz tenant.Authorizer
	basePath    string
	nav         []shell.NavItem
	title       string
	csrfKey     []byte
	locales     locale.Set          // configured i18n locales; zero value = single-locale
	profileCfg  shell.ProfileConfig // static defaults for the sidebar profile block
	resources   []Resource          // registered Resources, in Register order
	// pagePaths holds every URL suffix MountPage has claimed (Path and each
	// Alias), so a route mounted LATER can tell whether it would shadow one.
	// Needed because MountPage runs during setup while finalize() runs after
	// it: the panel-wide Trash cannot see a conflicting page any other way,
	// and net/http only rejects a byte-identical pattern — MountPage's
	// "{suffix}/{$}" and the Trash's bare "/trash" do not collide in the mux,
	// so the consumer's page is silently shadowed instead.
	pagePaths []string

	// indexOverride is set once via MountPage(PageSpec{Path: ""}) before the
	// mux is finalized; it replaces the default handleIndex at GET {basePath}/{$}.
	// Written only during setup (MountPage); read exactly once, in finalize().
	indexOverride http.HandlerFunc
	// finalizeOnce guards the one-time index-route mount performed by
	// finalize(), invoked on the first Handler() call.
	finalizeOnce sync.Once
	// finalized is set true once finalize() has run. MountPage panics if
	// called after finalized is true: pages must be mounted before the first
	// Handler() call, so the routes the mux serves are fixed for its whole
	// lifetime (fail-closed rather than silently accepting a too-late mount).
	finalized bool
}

// SetProfile configures the static defaults for the sidebar profile block.
// For multi-user consumers (BcryptTOTPAuth), Name and Role are overlaid
// per-request from the live session (auth.SessionFrom), so typically only
// SettingsURL and LogoutURL need to be set here. For single-user consumers
// (HMACAuth), leaving ProfileConfig zero renders the bare Logout footer
// (backward-compatible default).
func (p *Panel) SetProfile(cfg shell.ProfileConfig) {
	p.profileCfg = cfg
}

// baseAuthenticator is the minimal session-auth contract Panel.auth and
// Config.Auth require: Require gates a handler behind a valid session,
// LoginHandler/LogoutHandler are mounted directly onto the panel mux in
// New() (cfg.Auth.LoginHandler() / cfg.Auth.LogoutHandler()). It is
// deliberately NOT auth.Authenticator — a 4-method superset that also
// demands Verified — because widening Config.Auth to that shape would
// silently reject any implementation (real or test double) that only
// provides these 3 methods. Keep this exactly 3 methods: New()'s mux-dispatch
// mount and guard()'s p.auth.Require() call depend on this shape resolving.
type baseAuthenticator interface {
	Require(http.HandlerFunc) http.HandlerFunc
	LoginHandler() http.Handler
	LogoutHandler() http.Handler
}

// sessionCookier is the optional interface implemented by authenticators that
// expose their session cookie name, used for CSRF double-submit binding.
type sessionCookier interface {
	SessionCookieName() string
}

// RoleAuthenticator is the optional capability an Authenticator implements to
// back role-gated resources. It is the security AUTHORITY for role checks:
//
//   - RequireRole is the route gate. It wraps a handler so only a session whose
//     role matches role (or the "owner" super-role) proceeds; everyone else
//     receives 403. This is the enforcement boundary — a route's access is
//     decided here, never derived from HasRole.
//   - HasRole is a read-only derivation used for nav-hiding (don't render a link
//     the operator cannot use). It must never be the only check guarding a
//     protected route; that is RequireRole's job.
//
// An authenticator that does not implement this interface cannot back a Resource
// with a non-empty RequiredRole: Register panics at startup (fail-closed) rather
// than mount the resource ungated.
// NOTE: HasRole is no longer nav-only. The panel-wide Trash page uses it to
// decide which resources' DELETED ROW BODIES are rendered to this session
// (trashResourcesFor), so an implementation whose HasRole is looser than its
// RequireRole shows rows behind a Вернуть button that then 403s — present,
// plausible, and never once working. Keep the two predicates in agreement.
type RoleAuthenticator interface {
	RequireRole(role string, next http.HandlerFunc) http.HandlerFunc
	HasRole(ctx context.Context, role string) bool
}

// Config holds Panel configuration.
type Config struct {
	Title    string
	BasePath string // e.g. "/admin". Defaults to "/admin".
	Auth     baseAuthenticator
	Resolver tenant.Resolver // nil = PathResolver{Segment:2}
	// TenantAuthorizer decides whether a resolved Tenant may be accessed by
	// the current session. nil (the default) resolves to
	// tenant.GlobalOnlyAuthorizer{} — fail-closed: allows the global tenant
	// (today's only reachable one) and denies every other tenant. New logs a
	// construction-time WARN when this defaults (see
	// tenantAuthzDefaultWarning) — the only runtime signal against a
	// silently permissive state once a second tenant becomes resolvable. A
	// real multi-tenant deployment must configure an explicit Authorizer.
	TenantAuthorizer tenant.Authorizer
	// CSRFKey is the HMAC signing key for CSRF double-submit tokens.
	// Required when any Resource has a non-nil Writer; omitting it causes
	// a panic at Register time (fail-closed configuration).
	// Must be at least 32 bytes.
	CSRFKey []byte
	// Locales declares the deployment's i18n locale set (Default + Available).
	// The zero value (no Available) means single-locale: forms render every
	// field, no locale switcher is shown, and locale.From(ctx) is the empty
	// Locale (apps treat it as the default / untranslated value).
	// When more than one locale is configured, edit forms show a locale
	// switcher and Translatable fields are edited per locale; the active locale
	// is handed to Writer.Load / Writer.Save via context (locale.From(ctx)).
	Locales locale.Set
}

// New creates a Panel with the given config.
func New(cfg Config) *Panel {
	bp := cfg.BasePath
	if bp == "" {
		bp = "/admin"
	}
	title := cfg.Title
	if title == "" {
		title = "Admin"
	}
	resolver := cfg.Resolver
	if resolver == nil {
		resolver = tenant.PathResolver{Segment: 2}
	}
	tenantAuthz := cfg.TenantAuthorizer
	if tenantAuthz == nil {
		tenantAuthz = tenant.GlobalOnlyAuthorizer{}
		// resolver is always set above (defaulted to PathResolver{Segment:2}
		// when unconfigured), and every Resolver this repo ships can
		// plausibly resolve a non-global tenant — there is no "resolver
		// incapable of non-global" case to gate on, so an unconfigured
		// TenantAuthorizer is loud unconditionally rather than only for some
		// resolver configurations.
		slog.Warn(tenantAuthzDefaultWarning, "resolver", fmt.Sprintf("%T", resolver))
	}
	p := &Panel{
		mux:         http.NewServeMux(),
		auth:        cfg.Auth,
		resolver:    resolver,
		tenantAuthz: tenantAuthz,
		basePath:    bp,
		title:       title,
		csrfKey:     cfg.CSRFKey,
		locales:     cfg.Locales,
	}
	// Mount standard routes.
	p.mux.Handle(bp+"/static/", http.StripPrefix(bp+"/static", shell.StaticHandler()))
	p.mux.Handle(bp+"/login", cfg.Auth.LoginHandler())
	p.mux.Handle(bp+"/logout", cfg.Auth.LogoutHandler())
	// Index route (GET bp+"/{$}") is registered by finalize(), on the first
	// Handler() call — not here. A MountPage(PageSpec{Path: ""}) custom index
	// must be able to claim that pattern before it's mounted; registering it
	// eagerly here would collide with MountPage's own registration (the mux
	// panics on a duplicate "GET {$}" pattern).
	return p
}

// Handler returns the http.Handler for the entire admin surface.
// Mount at the admin path (e.g. /admin/) in your app mux.
//
// The first call finalizes the mux: it mounts the index route (a MountPage
// custom index if one was registered via PageSpec{Path: ""}, otherwise the
// default handleIndex). MountPage calls after Handler() has been called panic.
//
// The returned handler is wrapped with withTenantResolution — the single
// composition point where p.resolver actually runs (see the tenant package
// doc's routing-mutation note). Tenant AUTHORIZATION is enforced separately,
// per-route, by guard (via requireTenant); resolution here only decides which
// tenant a request names.
func (p *Panel) Handler() http.Handler {
	p.finalize()
	return withBasePath(p.basePath, withTenantResolution(p.resolver, p.mux))
}

// basePathCtxKey carries the panel's mount prefix on the request context.
type basePathCtxKey struct{}

// BasePathFrom returns the mount prefix of the panel serving this request —
// "/admin" by default, or whatever Config.BasePath was set to. It returns ""
// for a context that did not come from a panel request.
//
// This exists because CrossLinkCell and FilterLinkCell both take a basePath
// and their docs tell consumers to pass the panel's "rather than a hardcoded
// /admin so the link stays correct under custom mounts" — while a Lister,
// Detailer or Writer had no way to obtain it. Every consumer therefore
// hardcoded "/admin", which is exactly the bug CrossLinkCell was promoted into
// this framework to fix. Asking for a value the framework does not hand out is
// not a rule, it is a trap.
//
// Read it from the ctx those closures already receive:
//
//	Detailer: func(ctx context.Context, _ *http.Request, id string) ([]DetailSection, error) {
//	    href := FilterLinkCell(BasePathFrom(ctx), "orders", "client_id", id, "Orders")
//	    …
//	}
func BasePathFrom(ctx context.Context) string {
	v, _ := ctx.Value(basePathCtxKey{}).(string)
	return v
}

// withBasePath stores the panel's mount prefix on every request it serves.
// Wrapped OUTSIDE tenant resolution so the value is present no matter which
// route or middleware runs next — the basePath is a property of the panel, not
// of the request, and nothing downstream can change it.
func withBasePath(bp string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), basePathCtxKey{}, bp)))
	})
}

// Resources returns the registered Resources in registration order.
// The returned slice is a copy; callers may freely iterate or mutate it
// without affecting the Panel's internal state.
func (p *Panel) Resources() []Resource {
	out := make([]Resource, len(p.resources))
	copy(out, p.resources)
	return out
}

// withTenantResolution wraps next with the panel's single tenant-resolution
// composition point: resolve a Tenant from the request via resolver, strip a
// concrete tenant.PathResolver's /tenant/{slug} marker pair from the path so
// the mux can match the underlying resource route (mirrors the
// http.StripPrefix idiom used for the static-asset mount in New — shallow-copy
// the request and URL rather than mutate the caller's), store the resolved
// Tenant on the request context via tenant.WithTenant, then serve.
//
// Resolve runs BEFORE strip, on the UNSTRIPPED path, which makes this
// idempotent: wrapping an already-resolved request a second time (e.g.
// go-grad's outer tenant.Middleware during the Phase 1a/1b rollout interim)
// re-derives the identical Tenant from the same marker-guarded path shape,
// and the second strip is a no-op on the already-stripped path.
//
// Only a concrete tenant.PathResolver is stripped — a type-switch, not a new
// exported interface, since exactly two Resolver implementations exist
// repo-wide (see the P1a ADR). tenant.SubdomainResolver carries no path
// prefix to remove.
//
// Matches both tenant.PathResolver and *tenant.PathResolver: Resolve has a
// value receiver, so a config-time &tenant.PathResolver{...} also satisfies
// tenant.Resolver and is a realistic construction — matching the value form
// only would silently skip the strip step for it (tenant-prefixed routes
// would 404 at mux dispatch instead of matching; fail-closed, but a latent
// footgun worth avoiding outright).
func withTenantResolution(resolver tenant.Resolver, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t := resolver.Resolve(r)
		ctx := tenant.WithTenant(r.Context(), t)

		var pr tenant.PathResolver
		switch v := resolver.(type) {
		case tenant.PathResolver:
			pr = v
		case *tenant.PathResolver:
			if v == nil {
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			pr = *v
		default:
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		stripped, changed := pr.StripPrefix(r.URL.Path)
		if !changed {
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		// Shallow-copy r and r.URL before rewriting Path — never mutate the
		// caller's *http.Request (mirrors net/http.StripPrefix).
		r2 := new(http.Request)
		*r2 = *r
		r2.URL = new(url.URL)
		*r2.URL = *r.URL
		r2.URL.Path = stripped
		next.ServeHTTP(w, r2.WithContext(ctx))
	})
}

// handleIndex serves GET {basePath}/{$} (the bare base path).
// Authenticated: redirects to the first registered resource with a non-empty URL
// that the current operator is permitted to access (respects RequiredRole and
// Visible filters, so an operator is never bounced from the root into a resource
// they'll 403 on).
// If no accessible resources are registered, returns a minimal 200 HTML page.
// Unauthenticated: handled by p.auth.Require before this is reached.
func (p *Panel) handleIndex(w http.ResponseWriter, r *http.Request) {
	for _, n := range p.navItemsFor(r.Context(), "") {
		// Skip group headers (empty URL).
		if n.URL == "" {
			continue
		}
		http.Redirect(w, r, n.URL, http.StatusSeeOther)
		return
	}
	// No accessible resources registered — return a minimal placeholder page.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte("<html><body><h1>Admin</h1><p>No resources registered yet.</p></body></html>"))
}

// NavItems returns a snapshot of registered nav items (for testing/introspection).
func (p *Panel) NavItems() []shell.NavItem {
	result := make([]shell.NavItem, len(p.nav))
	copy(result, p.nav)
	return result
}

// AddNav appends item to the panel's sidebar navigation. It must be called
// at setup time (not concurrently with other Panel mutations) and after
// the relevant Register calls if the caller wants the item to appear after
// resource entries. To place an item under a named group that isn't already
// present, emit a group-header NavItem{Group: "X"} before the link item(s).
//
// A header is recognised STRUCTURALLY — a Group with no URL — so the bare form
// above is enough. Register additionally stamps ID "group:<name>" on the
// headers it creates, which is what lets addNavLink find and reuse an existing
// group; a hand-rolled header without that ID renders identically but will not
// be reused, so a later Register for the same Group opens a second heading.
// Set ID: "group:"+name if you want a Register'd resource to join your group.
//
// Do NOT set both Group and URL on one item. shell.toNavGroups opens a new
// section for anything carrying a Group and files only Group-less items into a
// section's links, so such an item renders as a heading and its URL is silently
// discarded. A link that should sit under a section carries no Group of its
// own — its placement comes from following that section's header.
func (p *Panel) AddNav(item shell.NavItem) {
	p.nav = append(p.nav, item)
}

// NavItemsActive returns a snapshot of the panel's nav items with the item
// matching activeID marked Active. It is safe to call concurrently (returns
// a copy). Consumers rendering bespoke pages should use this instead of
// NavItems to get the active highlight.
func (p *Panel) NavItemsActive(activeID string) []shell.NavItem {
	items := make([]shell.NavItem, len(p.nav))
	copy(items, p.nav)
	for i := range items {
		items[i].Active = items[i].ID == activeID
	}
	return items
}

// Register mounts the resource's list handler and adds it to the sidebar nav.
// Panics at startup if Sort or Filter are misconfigured (fail-fast, not at runtime).
// When the resource has a Writer, also panics if:
//   - CSRFKey is empty or shorter than 32 bytes (fail-closed key floor, SEC-CR-001)
//   - the authenticator does not implement SessionCookieName() (fail-closed session binding)
//
// Mounted routes:
//
//	GET  {basePath}/{name}            — list page (full or htmx fragment)
//	GET  {basePath}/{name}/rows       — htmx row fragment only (sort/filter swap target)
//	GET  {basePath}/{name}/{id}       — detail/Show page (only when Detailer != nil; id=="new" → 404)
//	GET  {basePath}/{name}/new        — empty create form (only when Writer != nil)
//	GET  {basePath}/{name}/{id}/edit  — pre-populated edit form (only when Writer != nil; id=="new" → 404)
//	POST {basePath}/{name}/{id}/save  — save (id=="new" means create) (only when Writer != nil)
func Register(p *Panel, r Resource) {
	if err := r.Sort.Valid(); err != nil {
		panic(fmt.Sprintf("resource.Register %q: invalid Sort: %v", r.Name, err))
	}
	if err := r.Filter.Valid(); err != nil {
		panic(fmt.Sprintf("resource.Register %q: invalid Filter: %v", r.Name, err))
	}
	if r.Writer != nil {
		validateWriterConfig(p, r)
	}
	if r.SingleRow && r.Writer == nil {
		panic(fmt.Sprintf("resource.Register %q: SingleRow requires Writer to be non-nil", r.Name))
	}
	validateRoleConfig(p, r)
	validateViewsConfig(r)
	validateTrashConfig(r)
	validateRelationsConfig(&r)
	p.resources = append(p.resources, r)

	addNavEntry(p, r)

	listHandler := p.makeListHandler(r)
	mountListRoutes(p, r, listHandler)

	// Detailer route — mounted when Detailer OR FetchRow is configured.
	// If Detailer is nil but FetchRow is non-nil, synthesize an auto-Detailer
	// from Sort.Columns + FetchRow (see autoDetailer).
	if r.Detailer != nil {
		mountDetailRoute(p, r)
	} else if r.FetchRow != nil {
		mountDetailRoute(p, withAutoDetailer(r))
	}

	// Writer routes — only mounted when Writer is configured.
	if r.Writer != nil {
		mountWriterRoutes(p, r)
	}
}

// validateTrashConfig fails closed on a trash that cannot work.
//
// A TrashLister without a Restore renders rows behind a button that 403s — the
// exact shape of the bug this panel spent a week on, where the affordance was
// present, looked right, and had never once run. Without a Delete nothing can
// put a row into the trash in the first place, so the section can only ever be
// empty. Both are startup mistakes, so both are startup panics.
func validateTrashConfig(r Resource) {
	if r.TrashLister == nil {
		return
	}
	if r.Writer == nil || r.Writer.Delete == nil {
		panic(fmt.Sprintf("resource.Register %q: TrashLister requires Writer.Delete (nothing can fill the trash otherwise)", r.Name))
	}
	if r.Writer.Restore == nil {
		panic(fmt.Sprintf("resource.Register %q: TrashLister requires Writer.Restore (the trash would have no way back)", r.Name))
	}
}

// addNavEntry inserts a group-header NavItem (if r.Group is set and not already
// present) followed by the resource's own nav item. Called from Register only.
//
// The link item is placed NEXT TO its group header, not appended at the end of
// p.nav. When the header already exists, the link is inserted after the run of
// items already belonging to that group (the consecutive Group=="" items
// following the header). This makes sidebar grouping independent of
// registration order: an interleaved or out-of-order registration produces the
// same grouping as a contiguous one.
//
// Boundaries:
//   - Empty Group: append at end (preserves the anonymous pre-group bucket
//     and today's behaviour for group-less resources).
//   - Group header not yet present: append header then link at end — identical
//     to today's behaviour for a new group, so contiguous consumers see
//     byte-identical nav output.
//   - Group header present (created by Register or by a manual AddNav): insert
//     the link after the group's existing members, preserving registration
//     order within the group.
func addNavEntry(p *Panel, r Resource) {
	link := shell.NavItem{
		ID:           r.Name,
		Label:        r.Title,
		Icon:         r.Icon,
		URL:          p.basePath + "/" + r.Name,
		Badge:        r.Badge,
		Visible:      r.Visible,
		RequiredRole: r.RequiredRole,
	}
	addNavLink(p, link, r.Group)
}

// addNavLink places link under group, creating the group header when the group
// is new and inserting after the group's existing members when it is not.
//
// Extracted from addNavEntry so the panel-wide Trash link goes through the SAME
// rule as a resource's. A second insertion path is exactly how the Trash grew
// its own duplicate "System" heading beside an identically-named group a
// consumer had already declared.
func addNavLink(p *Panel, link shell.NavItem, group string) {
	if group == "" {
		p.nav = append(p.nav, link)
		return
	}
	groupKey := "group:" + group
	headerIdx := -1
	for i, n := range p.nav {
		if n.Group == group && n.ID == groupKey {
			headerIdx = i
			break
		}
	}
	if headerIdx < 0 {
		// New group: append header then link at end — same as today.
		p.nav = append(p.nav, shell.NavItem{ID: groupKey, Group: group}, link)
		return
	}
	// Header exists: insert the link after the group's existing members.
	// Scan forward past consecutive Group=="" items following the header.
	insertAt := headerIdx + 1
	for insertAt < len(p.nav) && p.nav[insertAt].Group == "" {
		insertAt++
	}
	p.nav = slices.Insert(p.nav, insertAt, link)
}

// mountListRoutes mounts the GET list and rows routes for r. Single-row
// resources get a redirect-to-edit handler at the list path; regular resources
// get the full list page. The rows fragment route is mounted identically in
// both cases.
func mountListRoutes(p *Panel, r Resource, listHandler func(http.ResponseWriter, *http.Request, []shell.NavItem, bool)) {
	listPath := p.basePath + "/" + r.Name
	rowsPath := p.basePath + "/" + r.Name + "/rows"

	if r.SingleRow {
		// Single-row resource: GET /{name} redirects to /{name}/{id}/edit
		// for the first (only) row. The "new" route is not mounted.
		p.mux.HandleFunc("GET "+listPath, p.guard(r.RequiredRole, func(w http.ResponseWriter, req *http.Request) {
			ctx := req.Context()
			t := tenant.From(ctx)
			rows, _, err := r.Lister(ctx, ListQuery{Tenant: t, Limit: 1})
			if err != nil {
				slog.Error("resource: single-row lister failed", "resource", r.Name, "err", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			if len(rows) == 0 {
				// No row yet — render a message. The consumer should seed the row.
				nav := p.activeNav(ctx, r.Name)
				shell.SecurityHeaders(w)
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				listHandler(w, req, nav, false)
				return
			}
			editURL := p.basePath + "/" + r.Name + "/" + rows[0].ID + "/edit"
			if render.IsHTMX(req) {
				w.Header().Set("HX-Redirect", editURL)
				w.WriteHeader(http.StatusOK)
				return
			}
			http.Redirect(w, req, editURL, http.StatusSeeOther)
		}))
		// /rows still works for htmx fragments if needed.
		p.mux.HandleFunc("GET "+rowsPath, p.guard(r.RequiredRole, func(w http.ResponseWriter, req *http.Request) {
			shell.SecurityHeaders(w)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			listHandler(w, req, nil, true)
		}))
	} else {
		p.mux.HandleFunc("GET "+listPath, p.guard(r.RequiredRole, func(w http.ResponseWriter, req *http.Request) {
			nav := p.activeNav(req.Context(), r.Name)
			shell.SecurityHeaders(w)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			listHandler(w, req, nav, false)
		}))
		p.mux.HandleFunc("GET "+rowsPath, p.guard(r.RequiredRole, func(w http.ResponseWriter, req *http.Request) {
			shell.SecurityHeaders(w)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			listHandler(w, req, nil, true)
		}))
	}
}

// validateCSRFConfig panics (fail-closed) unless p is configured for
// CSRF-protected write routes: Config.CSRFKey at least minCSRFKeyLen bytes,
// and an authenticator implementing SessionCookieName() (CSRF tokens bind to
// the session cookie). Shared by validateWriterConfig (Register's Writer
// path) and MountAction (action.go) — both ran this identical three-check
// gate, in the identical order, independently before this extraction.
//
// label identifies the caller in every panic, already %q-formatted by the
// caller (e.g. `resource.Register "items"`, `resource: MountAction
// "widget"`); qualifier is an optional caller-specific lead-in clause
// ("Writer is set but ", or "" for MountAction, which has no separate
// Writer concept); purpose completes "set CSRFKey to enable <purpose>"
// (e.g. "write forms", "actions").
//
// MountTOTPEnrollment (totp.go) enforces the same underlying invariant —
// CSRF needs a real key and a session-bound authenticator — but was written
// independently, with its own check order (SessionCookieName before key
// length) and its own message wording; it is intentionally NOT routed
// through this helper rather than force a shared order/wording onto code
// that was never actually copy-pasted from either caller here.
func validateCSRFConfig(p *Panel, label, qualifier, purpose string) {
	if len(p.csrfKey) == 0 {
		panic(fmt.Sprintf("%s: %sConfig.CSRFKey is empty — set CSRFKey to enable %s (fail-closed)", label, qualifier, purpose))
	}
	if len(p.csrfKey) < minCSRFKeyLen {
		panic(fmt.Sprintf("%s: Config.CSRFKey must be at least %d bytes, got %d (fail-closed, SEC-CR-001)", label, minCSRFKeyLen, len(p.csrfKey)))
	}
	if _, ok := p.auth.(sessionCookier); !ok {
		panic(fmt.Sprintf("%s: %sthe authenticator does not implement SessionCookieName() — CSRF tokens cannot be bound to the session cookie (fail-closed)", label, qualifier))
	}
}

// validateWriterConfig panics if the Writer configuration is invalid.
// Called at Register time — all checks are fail-closed.
func validateWriterConfig(p *Panel, r Resource) {
	validateCSRFConfig(p, fmt.Sprintf("resource.Register %q", r.Name), "Writer is set but ", "write forms")
	if err := r.Writer.Form.Valid(); err != nil {
		panic(fmt.Sprintf("resource.Register %q: invalid Writer.Form: %v", r.Name, err))
	}
}

// validateRoleConfig panics at Register time if the resource declares a
// non-empty RequiredRole but the authenticator cannot enforce it (does not
// implement RoleAuthenticator). Fail-closed: a role-gated resource must never
// mount against an authenticator that would serve it ungated.
func validateRoleConfig(p *Panel, r Resource) {
	if r.RequiredRole == "" {
		return
	}
	if _, ok := p.auth.(RoleAuthenticator); !ok {
		panic(fmt.Sprintf("resource.Register %q: RequiredRole %q is set but the authenticator does not implement RoleAuthenticator — role gating cannot be enforced (fail-closed)", r.Name, r.RequiredRole))
	}
}

// guard wraps h with the panel's authentication and, when requiredRole is
// non-empty, additionally enforces the role via the RoleAuthenticator
// capability. For an empty role it is exactly p.auth.Require(requireTenant(h))
// — no auth-flow behaviour change for resources that declare no RequiredRole,
// tenant-authz composed identically for every route regardless of role.
//
// A non-empty role requires p.auth to implement RoleAuthenticator. guard has
// two callers: Register, which pre-validates this via validateRoleConfig (so
// the panic below is defence-in-depth there — a failure means that guarantee
// was bypassed), and MountPage, which has no separate pre-check and relies on
// guard itself to validate eagerly at mount time. Either way we fail closed
// (panic at mount) rather than fail open.
//
// requireTenant is composed here as the innermost wrap around h — the LAST
// check before the resource handler runs. For an empty role it runs right
// after auth.Require's session check (there is no role check). For a
// role-gated route it runs AFTER the role check too: guard hands
// requireTenant(h) to RequireRole as RequireRole's OWN "next", so
// RequireRole's session+role checks execute first and only reach
// requireTenant (then h) once both pass. A single straight-line call —
// deliberately not inlined — so guard's own cyclomatic complexity is
// unchanged by tenant-authz; mirrors how role-gating is delegated to a
// separate RequireRole call rather than inlined branching.
func (p *Panel) guard(requiredRole string, h http.HandlerFunc) http.HandlerFunc {
	h = p.requireTenant(h)
	if requiredRole == "" {
		return p.auth.Require(h)
	}
	ra, ok := p.auth.(RoleAuthenticator)
	if !ok {
		panic(fmt.Sprintf("resource: guard called with role %q but the authenticator does not implement RoleAuthenticator (fail-closed)", requiredRole))
	}
	return ra.RequireRole(requiredRole, h)
}

// requireTenant returns a handler that enforces p.tenantAuthz against the
// Tenant resolved onto the request context (by withTenantResolution, which
// runs before mux dispatch — see Handler), denying with 403 when Authorized
// returns false OR a non-nil error. The two are treated identically: a
// transient authorizer failure must fail closed, never fail open.
//
// Composed by guard for every route (list/detail/rows-fragment/new/edit/save)
// regardless of RequiredRole — tenant-authz and role-authz are orthogonal
// gates, both must pass.
func (p *Panel) requireTenant(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t := tenant.From(r.Context())
		ok, err := p.tenantAuthz.Authorized(r.Context(), t)
		if err != nil || !ok {
			slog.WarnContext(r.Context(), "resource: tenant-denied",
				"tenant", t.CitySlug,
				"path", r.URL.Path,
				"method", r.Method,
				"err", err,
			)
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// ErrDetailNotFound signals that the id names no row: the framework answers
// 404 instead of 500.
//
// Honoured on EVERY id-addressed route — Detailer/FetchRow, and a Writer's
// Load, Save, Delete and Restore. The write routes used to map every error to
// 500, so a consumer could not distinguish "this row is gone" from "the store
// broke" and a stale /edit link read as an outage.
var ErrDetailNotFound = errors.New("resource: detail not found")

// mountDetailRoute mounts the GET {basePath}/{name}/{id} handler for a Detailer-enabled resource.
// Called only when r.Detailer != nil.
func mountDetailRoute(p *Panel, r Resource) {
	detailPath := p.basePath + "/" + r.Name + "/{id}"
	p.mux.HandleFunc("GET "+detailPath, p.guard(r.RequiredRole, detailHandler(p, r)))
}

// withAutoDetailer returns a shallow copy of r with a synthesized Detailer
// built from Sort.Columns + FetchRow. Used when the caller sets FetchRow but
// not Detailer — the auto-Detailer renders one DetailSection with a DetailItem
// per column (Label from Column.Label, Value from the FetchRow map keyed by
// Column.Key). Columns whose key is absent from the map render an empty value.
// The caller must NOT have set r.Detailer (this helper is only called in the
// else-branch of Register's Detailer check).
func withAutoDetailer(r Resource) Resource {
	r2 := r // shallow copy — closures and specs are shared, only Detailer is new
	r2.Detailer = func(ctx context.Context, _ *http.Request, id string) ([]DetailSection, error) {
		row, err := r.FetchRow(ctx, id)
		if err != nil {
			return nil, err
		}
		items := make([]DetailItem, 0, len(r.Sort.Columns))
		for _, col := range r.Sort.Columns {
			items = append(items, DetailItem{
				Label: col.Label,
				Value: row[col.Key],
			})
		}
		return []DetailSection{{Items: items}}, nil
	}
	return r2
}

// EffectiveDetailer returns r's Detailer, or a synthesized auto-Detailer built
// from Sort.Columns + FetchRow when Detailer is nil but FetchRow is non-nil.
// Returns nil when both are nil (no detail page). External callers (e.g. the
// mcp package) use this to decide whether a resource has a detail page and to
// invoke it, without duplicating the Detailer-vs-FetchRow precedence logic.
func EffectiveDetailer(r Resource) func(ctx context.Context, req *http.Request, id string) ([]DetailSection, error) {
	if r.Detailer != nil {
		return r.Detailer
	}
	if r.FetchRow != nil {
		return withAutoDetailer(r).Detailer
	}
	return nil
}

// detailHandler returns the handler for GET {basePath}/{name}/{id}.
// id=="new" is rejected with 404 (symmetric with the edit route).
// The Detailer closure is called to fetch the sections; go-panel renders the chrome.
func detailHandler(p *Panel, r Resource) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		shell.SecurityHeaders(w)
		id := req.PathValue("id")
		if id == idNew {
			http.NotFound(w, req)
			return
		}
		// Reject path suffixes that belong to other routes ("/edit", "/save").
		// The 1.22 mux will prefer exact-suffix patterns, but guard defensively.
		if strings.HasSuffix(id, "/edit") || strings.HasSuffix(id, "/save") {
			http.NotFound(w, req)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		sections, err := r.Detailer(req.Context(), req, id)
		if err != nil {
			if errors.Is(err, ErrDetailNotFound) {
				http.NotFound(w, req)
				return
			}
			slog.ErrorContext(req.Context(), "detailer error", "err", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		nav := p.activeNav(req.Context(), r.Name)
		d := detailPageData{
			Resource: r,
			ID:       id,
			Sections: sections,
			BasePath: p.basePath,
		}
		// Issue CSRF token for the delete button when Writer.Delete is configured.
		if r.Writer != nil && r.Writer.Delete != nil {
			sessVal := p.sessionValue(req)
			d.CSRFToken = csrf.Issue(p.csrfKey, sessVal, csrf.DefaultTTL)
		}
		content := detailPageContent(d)
		layoutComp := shell.Layout(p.title, nav, content)
		renderCtx := shell.ContextWithChrome(req.Context(), p.chromeStateFrom(req))
		if err := layoutComp.Render(renderCtx, w); err != nil {
			slog.Error("resource: render detail page", "resource", r.Name, "id", strconv.Quote(sanitizeForLog(id)), "err", err)
			http.Error(w, "render failed", http.StatusInternalServerError)
		}
	}
}

// mountWriterRoutes mounts the create/edit/save handler triplet for a Writer-enabled resource.
// Called only when r.Writer != nil and all pre-conditions (key, session binding) have been verified.
func mountWriterRoutes(p *Panel, r Resource) {
	editPath := p.basePath + "/" + r.Name + "/{id}/edit"
	savePath := p.basePath + "/" + r.Name + "/{id}/save"

	// Single-row resources don't have a "new" route — the row already exists.
	if !r.SingleRow {
		newPath := p.basePath + "/" + r.Name + "/new"
		p.mux.HandleFunc("GET "+newPath, p.guard(r.RequiredRole, newFormHandler(p, r)))
	}
	p.mux.HandleFunc("GET "+editPath, p.guard(r.RequiredRole, editFormHandler(p, r)))
	p.mux.HandleFunc("POST "+savePath, p.guard(r.RequiredRole, saveHandler(p, r)))

	if r.Writer.Delete != nil {
		deletePath := p.basePath + "/" + r.Name + "/{id}/delete"
		p.mux.HandleFunc("POST "+deletePath, p.guard(r.RequiredRole, deleteHandler(p, r)))
	}
	// Mounted only alongside a Delete it can undo. A restore route without a
	// delete would be an endpoint nothing can reach.
	if r.Writer.Delete != nil && r.Writer.Restore != nil {
		restorePath := p.basePath + "/" + r.Name + "/{id}/restore"
		p.mux.HandleFunc("POST "+restorePath, p.guard(r.RequiredRole, restoreHandler(p, r)))
	}
}

// withResolvedForm returns a shallow copy of r whose Writer.Form has all
// OptionsFunc fields resolved into their static Options slices for the given
// context and tenant. The original r is not mutated.
// Returns an error if any OptionsFunc call fails; callers should 500.
func withResolvedForm(r Resource, ctx context.Context, t tenant.Tenant) (Resource, error) {
	resolved, err := r.Writer.Form.resolveOptions(ctx, t)
	if err != nil {
		return r, err
	}
	// Shallow-copy the Writer so we don't mutate the registered Resource.
	wCopy := *r.Writer
	wCopy.Form = resolved
	r.Writer = &wCopy
	return r, nil
}

// multiLocale reports whether more than one locale is configured.
func (p *Panel) multiLocale() bool {
	return p.locales.Multi()
}

// activeLocale resolves the locale to edit from the request's ?locale= query,
// validated against the configured set. An absent/unknown value resolves to
// the configured Default (or "" for a single-locale deployment).
func (p *Panel) activeLocale(req *http.Request) locale.Locale {
	return p.locales.Resolve(locale.Locale(req.URL.Query().Get("locale")))
}

// newFormHandler returns the handler for GET /{name}/new.
//
// Create always happens in the Default locale: a new record must capture its
// shared (non-translatable) fields, and you cannot translate a record that does
// not exist yet. Secondary-locale translations are added later via the edit
// form. The ?locale= query is therefore ignored here.
func newFormHandler(p *Panel, r Resource) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		shell.SecurityHeaders(w)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		ctx := locale.WithLocale(req.Context(), p.locales.Default)
		t := tenant.From(ctx)
		rr, err := withResolvedForm(r, ctx, t)
		if err != nil {
			slog.Error("resource: resolve options for new form", "resource", r.Name, "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		nav := p.activeNav(req.Context(), r.Name)
		tok := csrf.Issue(p.csrfKey, p.sessionValue(req), csrf.DefaultTTL)
		d := formPageData{
			Resource:     rr,
			Values:       map[string]string{},
			CSRFToken:    tok,
			BasePath:     p.basePath,
			Locales:      p.locales,
			ActiveLocale: p.locales.Default,
		}
		layoutComp := shell.Layout(p.title, nav, formPageContent(d))
		if err := layoutComp.Render(shell.ContextWithChrome(ctx, p.chromeStateFrom(req)), w); err != nil {
			slog.Error("resource: render new form", "resource", r.Name, "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
	}
}

// editFormHandler returns the handler for GET /{name}/{id}/edit.
// id=="new" is rejected with 404 (symmetric with save, which treats id=="new" as create).
func editFormHandler(p *Panel, r Resource) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		shell.SecurityHeaders(w)
		id := req.PathValue("id")
		if id == idNew {
			http.NotFound(w, req)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// Active locale from ?locale=; handed to Load via context so the app
		// returns that locale's translatable values (shared values are locale
		// independent).
		loc := p.activeLocale(req)
		ctx := locale.WithLocale(req.Context(), loc)
		t := tenant.From(ctx)
		values, err := r.Writer.Load(ctx, t, id)
		if err != nil {
			// An id that names no row is NOT FOUND, the same as on the detail
			// route. Until this existed a Writer had no way to say so: every
			// Load error became "load failed" 500, so a stale /edit link told
			// the operator the server had broken.
			if errors.Is(err, ErrDetailNotFound) {
				http.NotFound(w, req)
				return
			}
			slog.Error("resource: load for edit", "resource", r.Name, "err", err)
			http.Error(w, "load failed", http.StatusInternalServerError)
			return
		}
		rr, err := withResolvedForm(r, ctx, t)
		if err != nil {
			slog.Error("resource: resolve options for edit form", "resource", r.Name, "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		nav := p.activeNav(req.Context(), r.Name)
		tok := csrf.Issue(p.csrfKey, p.sessionValue(req), csrf.DefaultTTL)
		d := formPageData{
			Resource:     rr,
			ID:           id,
			Values:       values,
			CSRFToken:    tok,
			BasePath:     p.basePath,
			Locales:      p.locales,
			ActiveLocale: loc,
		}
		layoutComp := shell.Layout(p.title, nav, formPageContent(d))
		if err := layoutComp.Render(shell.ContextWithChrome(ctx, p.chromeStateFrom(req)), w); err != nil {
			slog.Error("resource: render edit form", "resource", r.Name, "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
	}
}

// saveHandler returns the handler for POST /{name}/{id}/save.
func saveHandler(p *Panel, r Resource) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		shell.SecurityHeaders(w)
		const maxFormBytes = 1 << 20 // 1 MB
		req.Body = http.MaxBytesReader(w, req.Body, maxFormBytes)
		if err := req.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		if !p.verifyCSRFToken(w, req, "resource: CSRF verification failed", "resource", r.Name) {
			return
		}

		id := req.PathValue("id")
		creating := id == idNew
		if creating {
			id = ""
		}

		// Active locale: create always lands in Default (symmetric with the new
		// form); edit honours ?locale=. The locale-filtered field set drives
		// collection AND validation so that shared fields, absent on a secondary
		// locale, are not spuriously "required".
		loc, multi := resolveSaveLocale(p, req, creating)
		ctx := locale.WithLocale(req.Context(), loc)

		// Resolve dynamic options fresh for this POST — whitelist must be live.
		t := tenant.From(ctx)
		rr, err := withResolvedForm(r, ctx, t)
		if err != nil {
			slog.Error("resource: resolve options for save", "resource", r.Name, "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		fields := rr.Writer.Form.localeFields(loc, p.locales.Default, multi)
		values := collectFormValues(req, fields)

		// On create, merge preset values (foreign keys from context, not the
		// form). Preset takes precedence over form values — prevents
		// hidden-field tampering. PresetValues is nil = no preset.
		if err := mergePresetValues(rr, ctx, t, creating, values); err != nil {
			slog.Error("resource: preset values failed", "resource", r.Name, "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		// Server-side validation over the active locale's field set.
		errs := validateFields(fields, values)
		if errs.hasErrors() {
			renderValidationErrors(w, req, p, rr, id, loc, values, errs)
			return
		}

		// Persist — a *SaveError is a domain validation failure (e.g. a
		// booking-overlap check in the Writer's own store): re-render the
		// form at 422 with the message on its field, same as field-level
		// validation above. Any other error is a genuine internal failure —
		// generic 500 body, detail logged server-side, never masked as a
		// user error.
		saveErr := rr.Writer.Save(ctx, t, id, values)
		if handleSaveError(w, req, p, rr, ctx, id, loc, values, saveErr) {
			return
		}
		if rr.Writer.AfterSave != nil {
			rr.Writer.AfterSave(ctx, id, nil)
		}

		// Redirect (PRG pattern). Custom redirect URL via RedirectAfterSave,
		// or default to the resource list page.
		postWriteRedirect(w, req, p, rr, ctx, id, rr.Writer.RedirectAfterSave)
	}
}

// resolveSaveLocale determines the active locale and multi-locale flag for a
// save request. Create always lands in Default (symmetric with the new form);
// edit honours ?locale=.
func resolveSaveLocale(p *Panel, req *http.Request, creating bool) (locale.Locale, bool) {
	if creating {
		return p.locales.Default, false
	}
	return p.activeLocale(req), p.multiLocale()
}

// mergePresetValues merges preset values (foreign keys from context, not the
// form) into values on create. Preset takes precedence over form values —
// prevents hidden-field tampering. PresetValues is nil = no preset. Returns
// an error if the PresetValues closure fails; the caller is responsible for
// the 500 response.
func mergePresetValues(r Resource, ctx context.Context, t tenant.Tenant, creating bool, values map[string]string) error {
	if !creating || r.Writer.PresetValues == nil {
		return nil
	}
	preset, err := r.Writer.PresetValues(ctx, t)
	if err != nil {
		return err
	}
	for k, v := range preset {
		values[k] = v
	}
	return nil
}

// handleSaveError processes a save error: a *SaveError is a domain validation
// failure that re-renders the form at 422 with the message on its field; any
// other error is a genuine internal failure (500, detail logged server-side).
// AfterSave is called with the error in both cases. Returns true when saveErr
// is non-nil (caller must return); false when there is no error.
func handleSaveError(w http.ResponseWriter, req *http.Request, p *Panel, r Resource, ctx context.Context, id string, loc locale.Locale, values map[string]string, saveErr error) bool {
	if saveErr == nil {
		return false
	}
	var se *SaveError
	if errors.As(saveErr, &se) {
		if r.Writer.AfterSave != nil {
			r.Writer.AfterSave(ctx, id, saveErr)
		}
		renderValidationErrors(w, req, p, r, id, loc, values, formErrors{se.Field: se.Message})
		return true
	}
	if r.Writer.AfterSave != nil {
		r.Writer.AfterSave(ctx, id, saveErr)
	}
	// Saving a row that is gone is NOT FOUND, not a server failure. The hook
	// still fires: it is told what happened either way.
	if errors.Is(saveErr, ErrDetailNotFound) {
		http.NotFound(w, req)
		return true
	}
	slog.Error("resource: save failed", "resource", r.Name, "err", saveErr)
	http.Error(w, "save failed", http.StatusInternalServerError)
	return true
}

// postWriteRedirect sends the PRG redirect after a successful write (save or
// delete). If redirectFn is non-nil and returns a non-empty string, that URL
// is used; otherwise the default is the resource list page. HTMX requests get
// an HX-Redirect header instead of an HTTP redirect.
func postWriteRedirect(w http.ResponseWriter, req *http.Request, p *Panel, r Resource, ctx context.Context, id string, redirectFn func(context.Context, string) string) {
	redirectURL := p.basePath + "/" + r.Name
	if redirectFn != nil {
		if custom := redirectFn(ctx, id); custom != "" {
			redirectURL = custom
		}
	}
	if render.IsHTMX(req) {
		w.Header().Set("HX-Redirect", redirectURL)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, req, redirectURL, http.StatusSeeOther)
}

// deleteHandler returns the handler for POST /{name}/{id}/delete.
// Only mounted when Writer.Delete is non-nil.
func deleteHandler(p *Panel, r Resource) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		shell.SecurityHeaders(w)
		const maxFormBytes = 1 << 20 // 1 MB
		req.Body = http.MaxBytesReader(w, req.Body, maxFormBytes)
		if err := req.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		if !p.verifyCSRFToken(w, req, "resource: CSRF verification failed on delete", "resource", r.Name) {
			return
		}

		id := req.PathValue("id")
		if id == "" || id == idNew {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}

		ctx := req.Context()
		t := tenant.From(ctx)

		deleteErr := r.Writer.Delete(ctx, t, id)
		if handleDeleteError(w, r, ctx, id, deleteErr) {
			return
		}
		if r.Writer.AfterDelete != nil {
			r.Writer.AfterDelete(ctx, id, nil)
		}

		// A recoverable delete from the table swaps the row in place: it dims
		// and offers the way back where Delete was. Anything else keeps the
		// original PRG behaviour, so a resource without Restore is untouched.
		if r.Writer.Restore != nil && render.IsHTMX(req) {
			renderDeletedRow(w, req, p, r, id)
			return
		}
		// From a detail page, hand the list the id so it can offer the same undo
		// as a toast. Only when the consumer has not chosen its own destination.
		if r.Writer.Restore != nil && r.Writer.RedirectAfterDelete == nil {
			http.Redirect(w, req, p.basePath+"/"+r.Name+"?restorable="+url.QueryEscape(id), http.StatusSeeOther)
			return
		}
		// Redirect (PRG pattern). Custom redirect URL via RedirectAfterDelete,
		// or default to the resource list page.
		postWriteRedirect(w, req, p, r, ctx, id, r.Writer.RedirectAfterDelete)
	}
}

// renderDeletedRow replaces the swapped-out <tr> with its deleted placeholder.
// A fresh CSRF token is issued for the undo button: the one that authorised the
// delete has been spent from the operator's point of view, and the restore is a
// separate state-changing POST that must carry its own.
func renderDeletedRow(w http.ResponseWriter, req *http.Request, p *Panel, r Resource, id string) {
	d := listPageData{
		Resource:  r,
		BasePath:  p.basePath,
		CSRFToken: csrf.Issue(p.csrfKey, p.sessionValue(req), csrf.DefaultTTL),
	}
	if err := deletedRow(d, id, r.Title).Render(req.Context(), w); err != nil {
		slog.Error("resource: render deleted row", "resource", r.Name, "id", sanitizeForLog(id), "err", err)
		http.Error(w, "render failed", http.StatusInternalServerError)
	}
}

// restoreHandler returns the handler for POST /{name}/{id}/restore.
// Only mounted when both Writer.Delete and Writer.Restore are non-nil.
func restoreHandler(p *Panel, r Resource) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		shell.SecurityHeaders(w)
		const maxFormBytes = 1 << 20 // 1 MB
		req.Body = http.MaxBytesReader(w, req.Body, maxFormBytes)
		if err := req.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if !p.verifyCSRFToken(w, req, "resource: CSRF verification failed on restore", "resource", r.Name) {
			return
		}
		id := req.PathValue("id")
		if id == "" || id == idNew {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		ctx := req.Context()
		if err := r.Writer.Restore(ctx, tenant.From(ctx), id); err != nil {
			if errors.Is(err, ErrDetailNotFound) {
				http.NotFound(w, req)
				return
			}
			slog.Error("resource: restore failed", "resource", r.Name, "id", sanitizeForLog(id), "err", err)
			http.Error(w, "restore failed", http.StatusInternalServerError)
			return
		}
		// Re-render rather than reconstruct the row. Rebuilding one row here
		// would need a single-row read the framework does not have, and would
		// be a second rendering path to keep in step with the list. Undo is
		// rare; a refresh is cheap and cannot drift.
		if render.IsHTMX(req) {
			w.Header().Set("HX-Refresh", "true")
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, req, p.basePath+"/"+r.Name, http.StatusSeeOther)
	}
}

// restorableIDParam sanitises the ?restorable= value before it reaches a URL in
// the rendered toast. Anything that is not a plain id is dropped rather than
// escaped: the parameter exists to name a row this admin just deleted, and a
// value that does not look like one is not worth rendering.
func restorableIDParam(v string) string {
	if v == "" || len(v) > 64 {
		return ""
	}
	for _, c := range v {
		if !isIDRune(c) {
			return ""
		}
	}
	return v
}

// isIDRune reports whether c may appear in a row id. Deliberately an allowlist:
// a blocklist of "dangerous" characters is the shape that keeps being one
// character short.
func isIDRune(c rune) bool {
	switch {
	case c >= '0' && c <= '9', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		return true
	case c == '-' || c == '_':
		return true
	default:
		return false
	}
}

// handleDeleteError logs a delete failure, calls AfterDelete with the error,
// and writes a 500 response. Returns true when deleteErr is non-nil (caller
// must return); false when there is no error.
func handleDeleteError(w http.ResponseWriter, r Resource, ctx context.Context, id string, deleteErr error) bool {
	if deleteErr == nil {
		return false
	}
	if r.Writer.AfterDelete != nil {
		r.Writer.AfterDelete(ctx, id, deleteErr)
	}
	// handleDeleteError has no *http.Request, so write the status directly;
	// http.NotFound only needs the request to decide nothing here.
	if errors.Is(deleteErr, ErrDetailNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return true
	}
	slog.Error("resource: delete failed", "resource", r.Name, "id", sanitizeForLog(id), "err", deleteErr)
	http.Error(w, "delete failed", http.StatusInternalServerError)
	return true
}

// collectFormValues reads submitted form values for the given fields (the
// active locale's field set). Checkboxes are normalised to checkboxTrue/"false"
// (unchecked = not submitted).
func collectFormValues(req *http.Request, fields []Field) map[string]string {
	values := make(map[string]string, len(fields))
	for _, fld := range fields {
		if fld.Kind == FieldCheckbox {
			val := req.FormValue(fld.Key)
			if val == checkboxTrue || val == "on" || val == "1" {
				values[fld.Key] = checkboxTrue
			} else {
				values[fld.Key] = "false"
			}
		} else {
			values[fld.Key] = req.FormValue(fld.Key)
		}
	}
	return values
}

// renderValidationErrors re-renders the form with field-level validation errors (HTTP 422).
// loc is the active locale so the re-rendered form (and its POST action) stay on
// the locale the editor submitted.
func renderValidationErrors(w http.ResponseWriter, req *http.Request, p *Panel, r Resource, id string, loc locale.Locale, values map[string]string, errs formErrors) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusUnprocessableEntity)
	nav := p.activeNav(req.Context(), r.Name)
	tok := csrf.Issue(p.csrfKey, p.sessionValue(req), csrf.DefaultTTL)
	d := formPageData{
		Resource:     r,
		ID:           id,
		Values:       values,
		Errors:       errs,
		CSRFToken:    tok,
		BasePath:     p.basePath,
		Locales:      p.locales,
		ActiveLocale: loc,
	}
	layoutComp := shell.Layout(p.title, nav, formPageContent(d))
	if err := layoutComp.Render(shell.ContextWithChrome(req.Context(), p.chromeStateFrom(req)), w); err != nil {
		slog.Error("resource: render validation errors", "resource", r.Name, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// sessionValue extracts the session cookie value from the request.
// Used as the binding input for CSRF tokens.
// The authenticator must implement SessionCookieName() — enforced at Register time.
func (p *Panel) sessionValue(r *http.Request) string {
	sc := p.auth.(sessionCookier)
	c, err := r.Cookie(sc.SessionCookieName())
	if err != nil {
		return ""
	}
	return c.Value
}

// verifyCSRFToken checks r's submitted CSRF token (csrf.FormField) against
// p.csrfKey and the session bound to r's session cookie — the verification
// core every write path in the framework shares (saveHandler, MountAction's
// csrfProtect in action.go, and the TOTP enrollment lifecycle's verifyCSRF
// in totp_handlers.go). r must already be through a successful ParseForm;
// callers parse (and cap) the body themselves first — the cap varies
// (saveHandler and csrfProtect both use a 1MB ceiling; TOTP's parseForm
// uses the much smaller maxTOTPFormBytes = 4096, deliberately, see its own
// doc) — so parsing stays each caller's own responsibility.
//
// Returns true when the token is valid. On failure it writes the response
// itself (403, a generic body — detail logged server-side) and returns
// false; the caller must stop processing immediately. logMsg and logFields
// let each caller keep its own log identity — e.g. saveHandler logs
// "resource"=Resource.Name, csrfProtect logs "path"=r.URL.Path, TOTP's logs
// neither extra field — verifyCSRFToken always appends "err" itself, last,
// matching every existing call site's field order.
func (p *Panel) verifyCSRFToken(w http.ResponseWriter, r *http.Request, logMsg string, logFields ...any) bool {
	token := r.FormValue(csrf.FormField) //nolint:gosec // G120 false positive: every caller (saveHandler, csrfProtect, totpEnrollment.verifyCSRF) already wraps r.Body in http.MaxBytesReader via ParseForm/parseForm before calling verifyCSRFToken; gosec's check doesn't trace through the caller
	if err := csrf.Verify(p.csrfKey, p.sessionValue(r), token); err != nil {
		slog.WarnContext(r.Context(), logMsg, append(logFields, "err", err)...)
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return false
	}
	return true
}

// navItemsFor returns the context-filtered, active-marked nav list for rendering.
// It applies two orthogonal filters in the resource layer so the shell template
// receives a pre-filtered list and never evaluates auth or closures itself:
//
//  1. RequiredRole-derived hide: a nav item whose RequiredRole is set is dropped
//     unless the session on ctx satisfies the role via RoleAuthenticator.HasRole.
//     This auto-derives nav-hiding from the same role used by the route gate, so
//     the two can never drift apart (the consumer declares the role once in
//     Resource.RequiredRole; both enforcement and hiding derive from it).
//
//  2. Visible cosmetic predicate: a nav item whose Visible closure returns false
//     for ctx is dropped. Visible is COSMETIC ONLY — it hides the nav link but
//     does NOT gate the route. An item hidden by Visible is still reachable by
//     direct URL; use RequiredRole to enforce access.
//
// Group headers (Group != "") are never dropped by these filters: they carry no
// route and have no RequiredRole/Visible themselves.
//
// NavItemsActive (public, unfiltered) is kept unchanged for introspection/tests.
func (p *Panel) navItemsFor(ctx context.Context, activeID string) []shell.NavItem {
	// Type-assert once. nil when auth lacks the RoleAuthenticator capability;
	// that can only happen for items with RequiredRole="" (validateRoleConfig
	// at Register panics otherwise), so nil is safe — those items pass through.
	ra, _ := p.auth.(RoleAuthenticator)

	// One pass, deciding each LINK exactly once. Visible is consumer code with
	// no caching helper — unlike Badge, which the docs tell you to wrap in
	// shell.CachedBadge — so asking a header to re-derive its members' state
	// would call every consumer closure once per group as well as once for
	// itself. Measured before this pass existed: 4 calls per render where main
	// made 1.
	visible := make([]bool, len(p.nav))
	for i, item := range p.nav {
		if isNavHeader(item) {
			continue // decided below, from its members
		}
		visible[i] = navLinkVisible(ctx, item, ra)
	}

	out := make([]shell.NavItem, 0, len(p.nav))
	for i, item := range p.nav {
		// A header whose every member was filtered out is dropped with them.
		// Before the Trash nothing could empty a group, because a group existed
		// only if a resource declared it; the Trash is the first nav item whose
		// whole group can vanish for one operator, and a bare heading over
		// nothing reads as a section that failed to load.
		if isNavHeader(item) {
			if !headerHasVisibleMember(p.nav, visible, i) {
				continue
			}
			out = append(out, item)
			continue
		}
		if !visible[i] {
			continue
		}
		item.Active = item.ID == activeID
		out = append(out, item)
	}
	return out
}

// isNavHeader reports whether item is a group header: a Group with no URL.
//
// Structural, NOT by ID. Register's headers carry ID "group:<name>" but
// AddNav's documented shape — NavItem{Group: "X"} — carries none, and matching
// on the ID silently deleted every hand-rolled heading while leaving its links
// behind to be absorbed into the preceding group. This is the same shape
// navItemsFor has always used to recognise a header.
func isNavHeader(item shell.NavItem) bool {
	return item.Group != "" && item.URL == ""
}

// navLinkVisible applies the two nav filters to one link: the RequiredRole gate
// and the cosmetic Visible predicate.
func navLinkVisible(ctx context.Context, item shell.NavItem, ra RoleAuthenticator) bool {
	if item.RequiredRole != "" && (ra == nil || !ra.HasRole(ctx, item.RequiredRole)) {
		return false
	}
	if item.Visible != nil && !item.Visible(ctx) {
		return false
	}
	return true
}

// headerHasVisibleMember reports whether any link filed under the header at idx
// survived this session's filters.
//
// The member run ends at the next item carrying a Group — the SAME test
// shell.toNavGroups uses to open a new bucket, and deliberately NOT isNavHeader's.
// The two disagree for an item carrying both a Group and a URL: isNavHeader calls
// it a link, because it has one, while toNavGroups opens a bucket for it and then
// files it into no bucket at all. Counting such an item as a member would keep the
// previous heading alive on the strength of something that renders elsewhere —
// measured, that left a heading with nothing beneath it, which is the exact
// picture this rule exists to prevent.
//
// Agreeing with toNavGroups is what makes the rule correct rather than merely
// plausible: a heading survives exactly when something will render under it.
func headerHasVisibleMember(nav []shell.NavItem, visible []bool, idx int) bool {
	for i := idx + 1; i < len(nav) && nav[i].Group == ""; i++ {
		if visible[i] {
			return true
		}
	}
	return false
}

// activeNav returns a context-filtered, active-marked copy of the nav list.
// Uses navItemsFor so role-gated and Visible-hidden items are excluded.
func (p *Panel) activeNav(ctx context.Context, activeID string) []shell.NavItem {
	return p.navItemsFor(ctx, activeID)
}

// validateViewsConfig fails fast on a malformed Views set. An empty key would
// render a chip that resolves to the default (so the chip would look selectable
// and do nothing), and a duplicate key makes the second chip unreachable —
// neither errors at runtime, both are silently wrong on screen, which is exactly
// the class Register's startup panics exist to convert into a boot failure.
func validateViewsConfig(r Resource) {
	seen := make(map[string]bool, len(r.Views))
	for i, v := range r.Views {
		if v.Key == "" {
			panic(fmt.Sprintf("resource.Register %q: Views[%d] has an empty Key", r.Name, i))
		}
		if seen[v.Key] {
			panic(fmt.Sprintf("resource.Register %q: Views[%d] duplicates Key %q", r.Name, i, v.Key))
		}
		seen[v.Key] = true
	}
}

// resolveView maps a raw URL value to one of r.Views' keys. An unknown or empty
// value resolves to the FIRST declared view, so a Lister never has to handle an
// unexpected key and a hand-edited URL degrades instead of erroring. Returns ""
// when the resource declares no views.
func (r Resource) resolveView(raw string) string {
	if len(r.Views) == 0 {
		return ""
	}
	for _, v := range r.Views {
		if v.Key == raw {
			return v.Key
		}
	}
	return r.Views[0].Key
}

// makeListHandler builds the handler func for a resource's list page.
func (p *Panel) makeListHandler(r Resource) func(http.ResponseWriter, *http.Request, []shell.NavItem, bool) {
	return func(w http.ResponseWriter, req *http.Request, nav []shell.NavItem, fragmentOnly bool) {
		ctx := req.Context()
		q := req.URL.Query()

		// Resolve sort state.
		sortState := r.Sort.Resolve(q.Get("sort"), q.Get("dir"))

		// Compose WHERE from filter params.
		// startArg starts at 1 since tenant scope will be appended AFTER filter args.
		whereConds, whereArgs := r.Filter.Where(q, 1)

		// Tenant scope — append after filter args so arg indices stay correct.
		t := tenant.From(ctx)
		var tenantCond string
		if r.Scope.Column != "" {
			tenantArg := len(whereArgs) + 1
			tc, ta := tenant.ScopeClause(r.Scope, t, tenantArg)
			if tc != "" {
				tenantCond = tc
				whereArgs = append(whereArgs, ta)
			}
		}

		// Combine WHERE conditions.
		finalConds := combineConds(whereConds, tenantCond)

		// Paging.
		page := max(1, parseIntParam(q.Get("page"), 1))
		pageSize := clampInt(parseIntParam(q.Get("per_page"), defaultPageSize), 1, maxPageSize)
		offset := (page - 1) * pageSize

		view := r.resolveView(q.Get("view"))

		lq := ListQuery{
			Sort:       sortState,
			WhereConds: finalConds,
			WhereArgs:  whereArgs,
			Tenant:     t,
			Limit:      pageSize,
			Offset:     offset,
			View:       view,
		}

		rows, total, err := r.Lister(ctx, lq)
		if err != nil {
			slog.Error("resource: list failed", "resource", r.Name, "err", err)
			http.Error(w, "list failed", http.StatusInternalServerError)
			return
		}

		// Phase 3a: resolve BelongsTo Relations, replacing raw FK cells with
		// XSS-safe CrossLinkCell anchors. Guarded by len(r.Relations) > 0 so
		// resources without Relations pay zero cost (backward compatible,
		// ADR-5). resolveRelations handles per-relation errors internally
		// (slog.Warn + raw FK per ADR-9); this top-level error is only for
		// unexpected failures.
		if len(r.Relations) > 0 {
			if err := resolveRelations(ctx, p, &r, rows); err != nil {
				slog.ErrorContext(ctx, "resource: resolveRelations failed", "resource", r.Name, "err", err)
			}
		}

		totalPages := max(1, (total+pageSize-1)/pageSize)

		data := listPageData{
			Resource:    r,
			Rows:        rows,
			SortState:   sortState,
			Page:        page,
			PageSize:    pageSize,
			Total:       total,
			TotalPages:  totalPages,
			BasePath:    p.basePath,
			QueryString: req.URL.RawQuery,
			ActiveView:  view,
			Selected:    q,
		}
		// The row-level Delete and the undo toast exist only where a delete can
		// be taken back. Issuing the token is what enables them: the templates
		// render nothing when it is empty, so this single condition governs the
		// whole affordance and there is no second place to keep in step.
		if r.Writer != nil && r.Writer.Delete != nil && r.Writer.Restore != nil {
			data.CSRFToken = csrf.Issue(p.csrfKey, p.sessionValue(req), csrf.DefaultTTL)
			data.RestorableID = restorableIDParam(q.Get("restorable"))
		}

		if fragmentOnly || render.IsHTMX(req) {
			renderListFragment(ctx, w, data, q.Get("append") == "1")
			return
		}

		content := listPageContent(data)
		layoutComp := shell.Layout(p.title, nav, content)
		if err := layoutComp.Render(shell.ContextWithChrome(ctx, p.chromeStateFrom(req)), w); err != nil {
			slog.Error("resource: render list page", "resource", r.Name, "err", err)
			http.Error(w, "render failed", http.StatusInternalServerError)
		}
	}
}

// --- helpers ---

// renderListFragment writes the htmx response for a list request: bare <tr>
// rows for a Load-more beforeend swap (append mode, with an OOB pagination
// replacement) or the full list-region fragment.
func renderListFragment(ctx context.Context, w http.ResponseWriter, data listPageData, appendMode bool) {
	c := listRowsFragment(data)
	if appendMode {
		c = listRowsAppend(data)
	}
	if err := c.Render(ctx, w); err != nil {
		slog.Error("resource: render list fragment", "resource", data.Resource.Name, "err", err)
		http.Error(w, "render failed", http.StatusInternalServerError)
	}
}

func combineConds(a, b string) string {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	switch {
	case a == "" && b == "":
		return ""
	case a == "":
		return b
	case b == "":
		return a
	default:
		return a + " AND " + b
	}
}

func parseIntParam(s string, def int) int {
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < 1 {
		return def
	}
	return v
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// sanitizeForLog strips control characters from s and caps its length so an
// attacker-controlled value (e.g. an id from the URL path) can be safely
// included in a structured log attribute without dropping it entirely.
//
// Two properties, both required for log injection defence (gosec G706):
//
//  1. No control characters — newlines and carriage returns let a caller forge
//     additional log records; ANSI escapes (ESC, C1 controls) can act on an
//     operator's terminal when they tail the log. All C0 controls except tab
//     (0x00-0x08, 0x0A-0x1F), DEL (0x7F), and C1 controls (0x80-0x9F) are
//     removed. Tab is kept because it is harmless in log output.
//
//  2. Bounded length — an unbounded id lets one request write an arbitrarily
//     large log line. The value is capped at maxLogIDLen runes.
//
// No existing in-house sanitiser covers both: go-kit strutil.Scrub only
// validates UTF-8 (replaces invalid bytes, keeps control chars); strutil.Truncate
// only caps length (keeps control chars). This function does both in one pass.
func sanitizeForLog(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		// Keep tab (0x09) — harmless in log output.
		// Drop all other C0 controls (0x00-0x08, 0x0A-0x1F), DEL (0x7F),
		// and C1 controls (0x80-0x9F).
		if r == '\t' || (r >= 0x20 && r != 0x7f && (r < 0x80 || r > 0x9f)) {
			b.WriteRune(r)
			n++
			if n >= maxLogIDLen {
				break
			}
		}
	}
	return b.String()
}
