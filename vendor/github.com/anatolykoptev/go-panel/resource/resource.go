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
	"strconv"
	"strings"

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

// DetailSection is one logical card / group on the Detail page.
// A section has an optional title and either a list of Items or a RawHTML block.
// RawHTML is for consumer-supplied pre-rendered HTML panels (e.g. a two-column
// fit-card); it must never contain raw DB/user text — escape before embedding.
type DetailSection struct {
	Title   string
	Items   []DetailItem
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
}

// Perms declares who may read or write this resource.
// Foundations: ReadAny means any authenticated session can read.
// Write perms are relevant only from Phase 2 (form/edit).
type Perms struct {
	// ReadAny allows any authenticated operator to read. Default.
	ReadAny bool
}

// ReadAny is the foundations default: any authenticated operator can read.
var ReadAny = Perms{ReadAny: true}

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
	Perms  Perms

	// RequiredRole gates every route of this resource behind the named role:
	// only a session whose role equals RequiredRole (or the "owner" super-role)
	// may reach the list, detail, and form routes; everyone else gets 403.
	// Empty (default) = no role gate: any authenticated operator may access,
	// preserving the foundational behaviour.
	//
	// A non-empty RequiredRole requires the configured authenticator to
	// implement RoleAuthenticator; Register panics at startup otherwise
	// (fail-closed). The same role drives nav-hiding via HasRole so the sidebar
	// does not surface a resource the operator cannot open.
	RequiredRole string

	// Lister fetches one page of rows. The kit hands it a safe ListQuery;
	// the app owns the row type + scan. go-panel never assumes a schema.
	Lister func(ctx context.Context, q ListQuery) (rows []Row, total int, err error)

	// Detailer enables a per-row Detail (Show) view at GET {basePath}/{name}/{id}.
	// Nil = no detail page (default — preserves existing behaviour).
	// When non-nil, Register mounts GET {basePath}/{name}/{id} and the template
	// renders the returned sections inside the standard shell.Layout.
	// id=="new" is rejected with 404 (symmetric with the edit route).
	// The closure must be safe to call concurrently (standard Go handler rules).
	// See DetailSection / DetailItem for the schema-agnostic shape.
	Detailer func(ctx context.Context, r *http.Request, id string) ([]DetailSection, error)

	// Writer enables create/edit forms. Nil = read-only (Phase 1 behaviour, default).
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
}

// Panel is the minimal composition root go-panel provides.
// It holds the mux, authenticator, tenant resolver, and the registered nav.
// Consumers create it via New() and call Handler() to get the http.Handler.
type Panel struct {
	mux  *http.ServeMux
	auth interface {
		Require(http.HandlerFunc) http.HandlerFunc
		LoginHandler() http.Handler
		LogoutHandler() http.Handler
	}
	resolver   tenant.Resolver
	basePath   string
	nav        []shell.NavItem
	title      string
	csrfKey    []byte
	locales    locale.Set        // configured i18n locales; zero value = single-locale
	profileCfg shell.ProfileConfig // static defaults for the sidebar profile block
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
type RoleAuthenticator interface {
	RequireRole(role string, next http.HandlerFunc) http.HandlerFunc
	HasRole(ctx context.Context, role string) bool
}

// Config holds Panel configuration.
type Config struct {
	Title    string
	BasePath string // e.g. "/admin". Defaults to "/admin".
	Auth     interface {
		Require(http.HandlerFunc) http.HandlerFunc
		LoginHandler() http.Handler
		LogoutHandler() http.Handler
	}
	Resolver tenant.Resolver // nil = PathResolver{Segment:2}
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
	p := &Panel{
		mux:      http.NewServeMux(),
		auth:     cfg.Auth,
		resolver: resolver,
		basePath: bp,
		title:    title,
		csrfKey:  cfg.CSRFKey,
		locales:  cfg.Locales,
	}
	// Mount standard routes.
	p.mux.Handle(bp+"/static/", http.StripPrefix(bp+"/static", shell.StaticHandler()))
	p.mux.Handle(bp+"/login", cfg.Auth.LoginHandler())
	p.mux.Handle(bp+"/logout", cfg.Auth.LogoutHandler())
	// Index route: redirect to the first real resource (or show a minimal page).
	p.mux.HandleFunc("GET "+bp+"/{$}", p.auth.Require(p.handleIndex))
	return p
}

// Handler returns the http.Handler for the entire admin surface.
// Mount at the admin path (e.g. /admin/) in your app mux.
func (p *Panel) Handler() http.Handler {
	return p.mux
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
// present, emit a group-header NavItem{Group: "X"} before the link item(s) —
// the same convention Register uses.
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
	validateRoleConfig(p, r)

	// Add nav entry.
	if r.Group != "" {
		// Insert group header if not already present.
		groupKey := "group:" + r.Group
		found := false
		for _, n := range p.nav {
			if n.Group == r.Group && n.ID == groupKey {
				found = true
				break
			}
		}
		if !found {
			p.nav = append(p.nav, shell.NavItem{
				ID:    groupKey,
				Group: r.Group,
			})
		}
	}
	p.nav = append(p.nav, shell.NavItem{
		ID:           r.Name,
		Label:        r.Title,
		Icon:         r.Icon,
		URL:          p.basePath + "/" + r.Name,
		Badge:        r.Badge,
		Visible:      r.Visible,
		RequiredRole: r.RequiredRole,
	})

	listPath := p.basePath + "/" + r.Name
	rowsPath := p.basePath + "/" + r.Name + "/rows"

	listHandler := p.makeListHandler(r)
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

	// Detailer route — only mounted when Detailer is configured.
	if r.Detailer != nil {
		mountDetailRoute(p, r)
	}

	// Writer routes — only mounted when Writer is configured.
	if r.Writer != nil {
		mountWriterRoutes(p, r)
	}
}

// validateWriterConfig panics if the Writer configuration is invalid.
// Called at Register time — all checks are fail-closed.
func validateWriterConfig(p *Panel, r Resource) {
	if len(p.csrfKey) == 0 {
		panic(fmt.Sprintf("resource.Register %q: Writer is set but Config.CSRFKey is empty — set CSRFKey to enable write forms (fail-closed)", r.Name))
	}
	if len(p.csrfKey) < minCSRFKeyLen {
		panic(fmt.Sprintf("resource.Register %q: Config.CSRFKey must be at least %d bytes, got %d (fail-closed, SEC-CR-001)", r.Name, minCSRFKeyLen, len(p.csrfKey)))
	}
	if _, ok := p.auth.(sessionCookier); !ok {
		panic(fmt.Sprintf("resource.Register %q: Writer is set but the authenticator does not implement SessionCookieName() — CSRF tokens cannot be bound to the session cookie (fail-closed)", r.Name))
	}
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
// capability. For an empty role it is exactly p.auth.Require — no behaviour
// change for resources that declare no RequiredRole.
//
// A non-empty role requires p.auth to implement RoleAuthenticator. That is
// guaranteed at Register time by validateRoleConfig, so the assertion here is
// defence-in-depth: a failure means the guarantee was bypassed, and we fail
// closed (panic at mount) rather than fail open.
func (p *Panel) guard(requiredRole string, h http.HandlerFunc) http.HandlerFunc {
	if requiredRole == "" {
		return p.auth.Require(h)
	}
	ra, ok := p.auth.(RoleAuthenticator)
	if !ok {
		panic(fmt.Sprintf("resource: guard called with role %q but the authenticator does not implement RoleAuthenticator (validateRoleConfig bypassed — fail-closed)", requiredRole))
	}
	return ra.RequireRole(requiredRole, h)
}

// ErrDetailNotFound may be returned by Detailer to signal a 404.
var ErrDetailNotFound = errors.New("resource: detail not found")

// mountDetailRoute mounts the GET {basePath}/{name}/{id} handler for a Detailer-enabled resource.
// Called only when r.Detailer != nil.
func mountDetailRoute(p *Panel, r Resource) {
	detailPath := p.basePath + "/" + r.Name + "/{id}"
	p.mux.HandleFunc("GET "+detailPath, p.guard(r.RequiredRole, detailHandler(p, r)))
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
		content := detailPageContent(d)
		layoutComp := shell.Layout(p.title, nav, content)
		renderCtx := shell.ContextWithChrome(req.Context(), p.chromeStateFrom(req))
		if err := layoutComp.Render(renderCtx, w); err != nil {
			slog.Error("resource: render detail page", "resource", r.Name, "id", id, "err", err)
			http.Error(w, "render failed", http.StatusInternalServerError)
		}
	}
}

// mountWriterRoutes mounts the create/edit/save handler triplet for a Writer-enabled resource.
// Called only when r.Writer != nil and all pre-conditions (key, session binding) have been verified.
func mountWriterRoutes(p *Panel, r Resource) {
	newPath := p.basePath + "/" + r.Name + "/new"
	editPath := p.basePath + "/" + r.Name + "/{id}/edit"
	savePath := p.basePath + "/" + r.Name + "/{id}/save"

	p.mux.HandleFunc("GET "+newPath, p.guard(r.RequiredRole, newFormHandler(p, r)))
	p.mux.HandleFunc("GET "+editPath, p.guard(r.RequiredRole, editFormHandler(p, r)))
	p.mux.HandleFunc("POST "+savePath, p.guard(r.RequiredRole, saveHandler(p, r)))
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
	return len(p.locales.Available) > 1
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

		// CSRF check — generic error body; detail logged server-side.
		token := req.FormValue(csrf.FormField)
		if err := csrf.Verify(p.csrfKey, p.sessionValue(req), token); err != nil {
			slog.Warn("resource: CSRF verification failed", "resource", r.Name, "err", err)
			http.Error(w, "invalid CSRF token", http.StatusForbidden)
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
		loc := p.locales.Default
		multi := false
		if !creating {
			loc = p.activeLocale(req)
			multi = p.multiLocale()
		}
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

		// Server-side validation over the active locale's field set.
		errs := validateFields(fields, values)
		if errs.hasErrors() {
			renderValidationErrors(w, req, p, rr, id, loc, values, errs)
			return
		}

		// Persist — generic error body; detail logged server-side.
		if err := rr.Writer.Save(ctx, t, id, values); err != nil {
			slog.Error("resource: save failed", "resource", r.Name, "err", err)
			http.Error(w, "save failed", http.StatusInternalServerError)
			return
		}

		// Redirect to list (PRG pattern).
		if render.IsHTMX(req) {
			w.Header().Set("HX-Redirect", p.basePath+"/"+r.Name)
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, req, p.basePath+"/"+r.Name, http.StatusSeeOther)
	}
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

	out := make([]shell.NavItem, 0, len(p.nav))
	for _, item := range p.nav {
		// Group headers are structural — never filtered.
		if item.Group != "" && item.URL == "" {
			out = append(out, item)
			continue
		}

		// RequiredRole-derived nav-hide.
		if item.RequiredRole != "" {
			if ra == nil || !ra.HasRole(ctx, item.RequiredRole) {
				continue // item is invisible to this session
			}
		}

		// Visible cosmetic predicate.
		if item.Visible != nil && !item.Visible(ctx) {
			continue
		}

		// Mark active.
		item.Active = item.ID == activeID
		out = append(out, item)
	}
	return out
}

// activeNav returns a context-filtered, active-marked copy of the nav list.
// Uses navItemsFor so role-gated and Visible-hidden items are excluded.
func (p *Panel) activeNav(ctx context.Context, activeID string) []shell.NavItem {
	return p.navItemsFor(ctx, activeID)
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

		lq := ListQuery{
			Sort:       sortState,
			WhereConds: finalConds,
			WhereArgs:  whereArgs,
			Tenant:     t,
			Limit:      pageSize,
			Offset:     offset,
		}

		rows, total, err := r.Lister(ctx, lq)
		if err != nil {
			http.Error(w, "list failed: "+err.Error(), http.StatusInternalServerError)
			return
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
		}

		if fragmentOnly || render.IsHTMX(req) {
			c := listRowsFragment(data)
			if err := c.Render(ctx, w); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}

		content := listPageContent(data)
		layoutComp := shell.Layout(p.title, nav, content)
		if err := layoutComp.Render(shell.ContextWithChrome(ctx, p.chromeStateFrom(req)), w); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

// --- helpers ---

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
