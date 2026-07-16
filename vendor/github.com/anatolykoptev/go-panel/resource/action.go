package resource

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/anatolykoptev/go-panel/shell"
)

// ActionSpec declares a POST-only admin action route, mounted into the
// Panel's own mux via MountAction — MountPage's POST-only twin, purpose-built
// for a state-changing form-post target rather than a page you navigate to.
//
// MountAction composes the identical auth/role/tenant guard MountPage and
// Register use (see PageSpec's doc), and additionally — UNLIKE MountPage —
// verifies the request's CSRF token and parses its body as a form BEFORE
// Handler runs: the same two checks saveHandler performs for a Writer-backed
// resource save (resource.go), generalized here for any action. A request
// that fails either check never reaches Handler; MountAction has already
// written the response (400 on a form-parse failure, 403 on a missing or
// invalid CSRF token).
//
// MountPage{Method: http.MethodPost} also mounts a POST route, but never
// verifies CSRF — Method only changes which verb reaches Handler. Use
// MountAction, not MountPage{Method: POST}, for any route that mutates
// state.
//
// Handler's response is entirely its own choice, exactly like
// PageSpec.Handler: most actions issue a PRG redirect
// (http.Redirect(w, r, dest, http.StatusSeeOther) — the pattern saveHandler
// and every hand-wrapped action route in consuming repos use today) or, for
// an HTMX request, an HX-Redirect header (see render.IsHTMX). MountAction
// does not prescribe either — the redirect target and shape vary too much
// across real callers (a fixed URL, the Referer, an error-decorated
// redirect) for one policy to fit; it only guarantees Handler sees an
// authenticated, authorized, CSRF-verified, form-parsed request.
type ActionSpec struct {
	// Path is the URL suffix under BasePath, leading/trailing slashes
	// trimmed, e.g. "jobs/{id}/rate" mounts POST {basePath}/jobs/{id}/rate.
	// Unlike PageSpec.Path, "" is not a valid Path here — there is no
	// index-action equivalent — MountAction panics if Path is empty.
	Path string

	// Handler serves the action once CSRF verification and form-parsing
	// have both succeeded. Required: MountAction panics if nil.
	Handler http.HandlerFunc

	// RequiredRole gates the action exactly like PageSpec.RequiredRole:
	// "" allows any authenticated session; a non-empty role requires the
	// configured authenticator to implement RoleAuthenticator, validated
	// EAGERLY at MountAction (fail-closed).
	RequiredRole string
}

// MountAction registers a POST-only admin action route into the Panel's own
// mux, wrapped with the same auth/role/tenant guard MountPage uses, plus
// CSRF verification and form-parsing (see ActionSpec's doc for the full
// contract). Like MountPage, it must be called before the first Handler()
// call, which finalizes the mux; calling MountAction afterward panics.
//
// MountAction requires Config.CSRFKey to be configured (>=32 bytes) and the
// authenticator to implement SessionCookieName(), both checked EAGERLY here
// (fail-closed) via the same validateCSRFConfig helper Register's
// validateWriterConfig calls. Unlike MountPage (which never touches CSRF),
// MountAction always verifies it, so a misconfiguration must fail at mount
// time, not panic on the first production POST (p.sessionValue asserts
// p.auth.(sessionCookier) without a comma-ok check).
func (p *Panel) MountAction(spec ActionSpec) {
	if p.finalized {
		panic("resource: MountAction called after Handler() — actions must be mounted before the mux is finalized")
	}
	if spec.Handler == nil {
		panic("resource: MountAction requires a non-nil Handler")
	}
	suffix := strings.Trim(spec.Path, "/")
	if suffix == "" {
		panic("resource: MountAction requires a non-empty Path")
	}
	validateCSRFConfig(p, fmt.Sprintf("resource: MountAction %q", spec.Path), "", "actions")

	guarded := p.guard(spec.RequiredRole, p.csrfProtect(spec.Handler))
	// No trailing "/{$}" — unlike MountPage's navigable GET pages, an action
	// is a POST form-target a developer hardcodes (a <form action> or fetch
	// call), never a typed-in browser URL, so trailing-slash canonicalization
	// serves no purpose here. This mirrors mountWriterRoutes' savePath, the
	// existing POST-only route convention in this file.
	p.mux.HandleFunc("POST "+p.basePath+"/"+suffix, guarded)
}

// csrfProtect wraps h with MountAction's standard pre-flight: set the
// security headers, cap and parse the request body as a form, then verify
// its CSRF token — the same sequence saveHandler runs for a Writer-backed
// resource save, generalized for any MountAction handler. h only runs once
// the request has cleanly parsed and carries a valid, session-bound CSRF
// token; on failure this writes the response itself and h never runs.
func (p *Panel) csrfProtect(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		shell.SecurityHeaders(w)

		const maxActionFormBytes = 1 << 20 // 1 MB, matches saveHandler's cap.
		r.Body = http.MaxBytesReader(w, r.Body, maxActionFormBytes)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		if !p.verifyCSRFToken(w, r, "resource: MountAction CSRF verification failed", "path", r.URL.Path) {
			return
		}

		h(w, r)
	}
}
