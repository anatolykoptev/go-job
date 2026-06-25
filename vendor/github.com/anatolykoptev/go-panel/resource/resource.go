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
	Detailer func(ctx context.Context, id string) ([]DetailSection, error)

	// Writer enables create/edit forms. Nil = read-only (Phase 1 behaviour, default).
	// When non-nil, CSRFKey must be set in Config (panic at Register if missing or < 32 bytes — fail-closed).
	Writer *Writer
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
	resolver tenant.Resolver
	basePath string
	nav      []shell.NavItem
	title    string
	csrfKey  []byte
	locales  locale.Set // configured i18n locales; zero value = single-locale
}

// sessionCookier is the optional interface implemented by authenticators that
// expose their session cookie name, used for CSRF double-submit binding.
type sessionCookier interface {
	SessionCookieName() string
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
// Authenticated: redirects to the first registered resource with a non-empty URL.
// If no resources are registered, returns a minimal 200 HTML page.
// Unauthenticated: handled by p.auth.Require before this is reached.
func (p *Panel) handleIndex(w http.ResponseWriter, r *http.Request) {
	for _, n := range p.nav {
		// Skip group headers (empty URL, ID prefixed with "group:").
		if n.URL == "" {
			continue
		}
		http.Redirect(w, r, n.URL, http.StatusSeeOther)
		return
	}
	// No resources registered yet — return a minimal placeholder page.
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
		ID:    r.Name,
		Label: r.Title,
		Icon:  r.Icon,
		URL:   p.basePath + "/" + r.Name,
	})

	listPath := p.basePath + "/" + r.Name
	rowsPath := p.basePath + "/" + r.Name + "/rows"

	listHandler := p.makeListHandler(r)
	p.mux.HandleFunc("GET "+listPath, p.auth.Require(func(w http.ResponseWriter, req *http.Request) {
		nav := p.activeNav(r.Name)
		shell.SecurityHeaders(w)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		listHandler(w, req, nav, false)
	}))
	p.mux.HandleFunc("GET "+rowsPath, p.auth.Require(func(w http.ResponseWriter, req *http.Request) {
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

// mountDetailRoute mounts the GET {basePath}/{name}/{id} handler for a Detailer-enabled resource.
// Called only when r.Detailer != nil.
func mountDetailRoute(p *Panel, r Resource) {
	detailPath := p.basePath + "/" + r.Name + "/{id}"
	p.mux.HandleFunc("GET "+detailPath, p.auth.Require(detailHandler(p, r)))
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
		sections, err := r.Detailer(req.Context(), id)
		if err != nil {
			slog.Error("resource: detailer failed", "resource", r.Name, "id", id, "err", err)
			http.Error(w, "detail failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		nav := p.activeNav(r.Name)
		d := detailPageData{
			Resource: r,
			ID:       id,
			Sections: sections,
			BasePath: p.basePath,
		}
		content := detailPageContent(d)
		layoutComp := shell.Layout(p.title, nav, content)
		if err := layoutComp.Render(req.Context(), w); err != nil {
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

	p.mux.HandleFunc("GET "+newPath, p.auth.Require(newFormHandler(p, r)))
	p.mux.HandleFunc("GET "+editPath, p.auth.Require(editFormHandler(p, r)))
	p.mux.HandleFunc("POST "+savePath, p.auth.Require(saveHandler(p, r)))
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
		nav := p.activeNav(r.Name)
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
		if err := layoutComp.Render(ctx, w); err != nil {
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
		nav := p.activeNav(r.Name)
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
		if err := layoutComp.Render(ctx, w); err != nil {
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
	nav := p.activeNav(r.Name)
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
	if err := layoutComp.Render(req.Context(), w); err != nil {
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

// activeNav returns a copy of the nav with the given resource ID marked active.
// It delegates to NavItemsActive to avoid duplication.
func (p *Panel) activeNav(activeID string) []shell.NavItem {
	return p.NavItemsActive(activeID)
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
		if err := layoutComp.Render(ctx, w); err != nil {
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
