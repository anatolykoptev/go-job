package resource

import (
	"net/http"
	"strings"
)

// PageSpec declares a custom admin page to mount into the Panel's own mux via
// MountPage, so Handler() serves it alongside the built-in resource routes —
// no external ServeMux wrapper required.
type PageSpec struct {
	// Path is the URL suffix under BasePath, leading/trailing slashes trimmed.
	// "" or "/" mounts the INDEX page (GET {basePath}/{$}), replacing the
	// default redirect-to-first-resource. A non-empty value, e.g.
	// "billing_preview", mounts a sub-page at GET {basePath}/billing_preview/{$}.
	Path string

	// Handler serves the page — typically by calling p.RenderPage. It is
	// wrapped with the Panel's guard (the same auth/role gate resources use)
	// before mounting. Required: MountPage panics if nil.
	Handler http.HandlerFunc

	// RequiredRole gates the page via the same mechanism as Resource.RequiredRole:
	// "" allows any authenticated session; a non-empty role requires the
	// configured authenticator to implement RoleAuthenticator, validated
	// EAGERLY at MountPage — panics otherwise (fail-closed).
	RequiredRole string

	// Aliases are extra exact-match GET suffixes serving the same guarded
	// handler (e.g. []string{"overview"} mounts GET {basePath}/overview).
	// Aliases are exact matches — no {$} pattern, no trailing-slash redirect.
	Aliases []string

	// Method is the HTTP method the route (Path and every Alias) is
	// registered for. Empty ("") defaults to GET — the sole behavior before
	// this field existed, so every pre-existing MountPage caller registers
	// byte-identical patterns to before. Set to a specific method (e.g.
	// http.MethodPost) to mount a state-changing action page — a form POST
	// target — that must not be reachable via GET.
	//
	// A GET-form + POST-action pair at the SAME Path is two separate
	// MountPage calls (Method:"" then Method:http.MethodPost): net/http's
	// ServeMux treats "GET path" and "POST path" as distinct patterns, so
	// both coexist without a duplicate-registration panic.
	//
	// Path:"" (the index override) must leave Method empty — MountPage
	// panics otherwise; the index route is always GET-navigated.
	//
	// MountPage never verifies CSRF regardless of Method — a
	// Method:http.MethodPost page's Handler is responsible for its own CSRF
	// check (see saveHandler in resource.go for the pattern this repo
	// already uses). For a state-changing route that should get CSRF
	// verification and form-parsing for free, mount it with MountAction
	// instead.
	Method string
}

// MountPage registers a custom admin page into the Panel's own mux, wrapped
// with the same auth/role guard Register uses for resources. It must be
// called before the first Handler() call, which finalizes the mux; calling
// MountPage afterward panics. Like AddNav, it must be called at setup time,
// not concurrently with other Panel mutations (Register, AddNav, other
// MountPage calls) — go-panel's setup phase is single-threaded by contract,
// not by lock.
//
// spec.Path == "" (or "/") mounts the page as the panel's index, replacing
// the default redirect-to-first-resource. Only one page may claim the index —
// a second Path:"" MountPage call panics.
//
// MountPage is routing only: it does not touch the sidebar nav. Consumers
// that want a nav entry for the page still call AddNav themselves (nav order
// matters to them and MountPage must not reorder it).
func (p *Panel) MountPage(spec PageSpec) {
	if p.finalized {
		panic("resource: MountPage called after Handler() — pages must be mounted before the mux is finalized")
	}
	if spec.Handler == nil {
		panic("resource: MountPage requires a non-nil Handler")
	}
	// guard() eagerly validates RequiredRole: it panics here (mount time) if
	// the authenticator can't back a non-empty role, rather than failing open
	// at request time.
	guarded := p.guard(spec.RequiredRole, spec.Handler)
	method := mountMethod(spec.Method)

	suffix := strings.Trim(spec.Path, "/")
	if suffix == "" {
		if spec.Method != "" {
			panic(`resource: MountPage Path:"" (index) must not set Method — the index route is always GET`)
		}
		if p.indexOverride != nil {
			panic(`resource: MountPage called with Path:"" twice — only one page may be the index`)
		}
		p.indexOverride = guarded
	} else {
		p.mux.HandleFunc(method+" "+p.basePath+"/"+suffix+"/{$}", guarded)
	}

	for _, alias := range spec.Aliases {
		a := strings.Trim(alias, "/")
		if a == "" {
			panic("resource: MountPage alias must not be empty")
		}
		p.mux.HandleFunc(method+" "+p.basePath+"/"+a, guarded)
	}
}

// mountMethod returns method, defaulting to GET when empty — see
// PageSpec.Method's doc for why the default preserves every pre-existing
// MountPage caller byte-for-byte.
func mountMethod(method string) string {
	if method == "" {
		return http.MethodGet
	}
	return method
}

// finalize mounts the index route exactly once: the MountPage-supplied
// override if MountPage(PageSpec{Path: ""}) was called, otherwise the
// default handleIndex. It runs on the first Handler() call so the mux is
// immutable after that point — no per-request mutable-field read (which
// would be a data race) — and finalized makes further MountPage calls fail
// closed instead of silently no-op'ing.
func (p *Panel) finalize() {
	p.finalizeOnce.Do(func() {
		h := p.indexOverride
		if h == nil {
			h = p.guard("", p.handleIndex)
		}
		p.mux.HandleFunc("GET "+p.basePath+"/{$}", h)
		p.finalized = true
	})
}
