// Package adminui serves go-job's operator admin (job/career tables) on a
// dedicated HTTP listener using the go-panel resource framework. It is
// fail-soft: New returns (nil,false) unless admin credentials are configured,
// so deploying before the env is wired changes nothing.
package adminui

import (
	"context"
	"net/http"
	"os"

	"github.com/anatolykoptev/go-panel/auth"
	"github.com/anatolykoptev/go-panel/resource"
	"github.com/anatolykoptev/go-panel/shell"
	"github.com/anatolykoptev/go_job/internal/accounts"
	"github.com/anatolykoptev/go_job/internal/engine/jobs/applications"
	"github.com/anatolykoptev/go_job/internal/hunt"
)

// adminBasePath is the base URL prefix for the admin panel.
// navIDLinkedin is the nav entry ID for the LinkedIn bespoke page (goconst: 4 occurrences).
const (
	adminBasePath = "/admin"
	navIDLinkedin = "linkedin"
)

// New builds the admin handler mounted at /admin. Returns (nil,nil,false) when
// ADMIN_HMAC_KEY (>=32 bytes) or ADMIN_PASSWORD is unset — admin disabled —
// or when the selected AUTH_DRIVER's own requirements fail (see auth_driver.go).
// acctStore/operatorID come from accounts.Bootstrap (the caller must run it
// before any account-consuming migration, ADR-6); acctStore may be nil without
// a database — only the AUTH_DRIVER=hmac rollback is possible then. keyStore
// feeds the self-serve /admin/keys surface (bcrypt mode only); nil under
// bcrypt omits those mounts defensively.
// The returned *resource.Panel can be used to expose Resources via MCP
// (see go-panel/mcp); nil when admin is disabled.
//
// Routing: an outer http.ServeMux wraps p.Handler() as a /admin/ catch-all.
// Bespoke 4-/5-segment routes (POST /rate, GET /download/{kind}) precede the
// panel catch-all and do not shadow go-panel's 3-segment routes (/rows, /{id}).
// GET /admin/jobs/{id} is served by go-panel via the Detailer (natural URL).
// The PUBLIC /admin/register route also lives on the outer mux (no session
// exists there — the panel guard would demand one), mounted only under the
// bcrypt driver.
func New(store *hunt.Store, authority *applications.Authority, acctStore *auth.PgxAccountStore, keyStore *accounts.KeyStore, operatorID string) (http.Handler, *resource.Panel, bool) {
	hmacKey := os.Getenv("ADMIN_HMAC_KEY")
	password := os.Getenv("ADMIN_PASSWORD")
	if len(hmacKey) < 32 || password == "" {
		return nil, nil, false
	}
	csrfKey := os.Getenv("ADMIN_CSRF_KEY")
	if len(csrfKey) < 32 {
		csrfKey = hmacKey // single-operator fallback; HMAC key is already >=32 bytes
	}

	adminUser := envOr("ADMIN_USERNAME", "admin")

	pool := store.Pool()
	d, ok := selectDriver(acctStore, pool, operatorID, hmacKey, password, adminUser)
	if !ok {
		return nil, nil, false
	}
	a := d.authn
	acctOf := d.accountOf
	cn := checkAuthCapabilities(a)
	p := resource.New(resource.Config{
		Title:            "go-job",
		BasePath:         adminBasePath,
		Auth:             a,
		CSRFKey:          []byte(csrfKey),
		Resolver:         d.resolver,
		TenantAuthorizer: d.authorizer,
	})

	// TOTP self-service enrollment ("my own 2FA") mounts only under bcrypt:
	// the routes resolve the acting account via auth.SessionFrom, which the
	// HMAC session never populates — under AUTH_DRIVER=hmac every enrollment
	// route would 401 anyway (ADR-17: no identity-dependent flows in rollback).
	if d.totpKey != nil {
		resource.MountTOTPEnrollment(p, resource.TOTPEnrollmentConfig{
			Store:             acctStore,
			TOTPEncryptionKey: d.totpKey,
			Issuer:            totpIssuer,
			PathPrefix:        "security/totp",
		})
	}

	// Shortlist (curated targets) is registered first so it appears first in the
	// Hunt nav group. resource.Register auto-routes /admin/shortlist and adds the
	// nav item — no manual p.AddNav call needed.
	resource.Register(p, shortlistResource(store, authority, []byte(csrfKey), acctOf))
	resource.Register(p, huntSettingsResource(store, acctOf))

	// Wire Detailer onto the jobs resource so GET /admin/jobs/{id} is served
	// by go-panel's framework detail page instead of a bespoke handler.
	jr := jobsResource(store, authority, []byte(csrfKey), acctOf)
	jr.Detailer = jobDetailer(pool, store, a, []byte(csrfKey), authority, acctOf)
	resource.Register(p, jr)

	resource.Register(p, bountiesResource(pool))
	resource.Register(p, freelanceResource(pool))
	resource.Register(p, securityResource(pool))
	resource.Register(p, contestsResource(pool))
	resource.Register(p, oversizeResource(pool, acctOf))

	// Resume resources — Writer-enabled CRUD via go-panel framework.
	resource.Register(p, personsResource(pool, acctOf))
	resource.Register(p, experiencesResource(pool, acctOf))
	resource.Register(p, skillsResource(pool, acctOf))
	resource.Register(p, achievementsResource(pool, acctOf))
	resource.Register(p, projectsResource(pool, acctOf))
	resource.Register(p, educationsResource(pool, acctOf))
	resource.Register(p, certificationsResource(pool, acctOf))
	resource.Register(p, domainsResource(pool, acctOf))
	resource.Register(p, methodologiesResource(pool, acctOf))

	// Upwork resources — Writer-enabled CRUD via go-panel framework.
	resource.Register(p, upworkOverviewResource(pool, acctOf))
	resource.Register(p, upworkSkillsResource(pool, acctOf))
	resource.Register(p, upworkCatalogResource(pool, acctOf))

	// Sidebar nav entries for bespoke pages (appear below auto-generated resource items).
	p.AddNav(shell.NavItem{Group: grpHunt})
	p.AddNav(shell.NavItem{ID: navIDDashboard, Label: "Dashboard", URL: adminBasePath + "/dashboard"})
	p.AddNav(shell.NavItem{Group: "Profile"})
	p.AddNav(shell.NavItem{ID: "resume", Label: "Resume", Icon: "📄", URL: "/admin/resume"})
	if d.selfServe && keyStore != nil {
		p.AddNav(shell.NavItem{ID: "keys", Label: "MCP Keys", Icon: "🔑", URL: "/admin/keys/"})
	}
	if d.selfServe {
		p.AddNav(shell.NavItem{ID: "password", Label: "Password", Icon: "🔒", URL: "/admin/password/"})
		// Visible is cosmetic only — the in-handler sess.Role=="admin" check
		// in accounts_admin.go is the real gate (RequiredRole stays banned:
		// it panics under hmac and is grep-gated by Makefile preflight).
		p.AddNav(shell.NavItem{ID: "accounts", Label: "Accounts", Icon: "👥", URL: "/admin/accounts/",
			Visible: func(ctx context.Context) bool {
				sess, ok := auth.SessionFrom(ctx)
				return ok && sess.Role == "admin"
			}})
	}
	p.AddNav(shell.NavItem{ID: navIDLinkedin, Label: "LinkedIn", Icon: "💼", URL: "/admin/linkedin"})
	p.AddNav(shell.NavItem{ID: navIDUpwork, Label: "Upwork", Icon: "🟢", URL: "/admin/upwork"})

	// Outer mux: bespoke 4-/5-segment routes first, panel catch-all last.
	// POST /rate and GET /download/{kind} are bespoke — not handled by Detailer.
	// GET /admin/jobs/{id} (natural 3-segment URL) is now served by go-panel.
	mux := http.NewServeMux()

	// P6 self-serve surfaces — bcrypt driver ONLY (d.selfServe is the named
	// contract; hmac never mounts them, see auth_driver.go header):
	//   - GET/POST /admin/register on the OUTER mux: the anonymous caller has
	//     no session, so the panel guard would deny/redirect before any
	//     handler ran — the route must sit beside /login, unguarded.
	//   - /admin/keys + /admin/accounts are session-scoped: MountPage/
	//     MountAction wrap them with the auth+CSRF guard for free.
	// keyStore nil (defensive — same pool lifecycle, can't happen today) omits
	// the keys surface; a nil store must never panic.
	if d.selfServe {
		reg := newRegisterHandler(pool, []byte(csrfKey))
		mux.HandleFunc("GET "+adminBasePath+"/register", reg.get)
		mux.HandleFunc("POST "+adminBasePath+"/register", reg.post)
		if keyStore != nil {
			p.MountPage(resource.PageSpec{Path: "keys", Aliases: []string{"keys"}, Handler: keysPage(p, keyStore, acctOf, []byte(csrfKey), cn.SessionCookieName())})
			p.MountAction(resource.ActionSpec{Path: "keys/mint", Handler: keysMint(p, acctStore, keyStore, acctOf, []byte(csrfKey), cn.SessionCookieName(), accounts.NewLoginLimiter())})
			p.MountAction(resource.ActionSpec{Path: "keys/{id}/revoke", Handler: keysRevoke(p, keyStore, acctOf, []byte(csrfKey), cn.SessionCookieName())})
		}
		p.MountPage(resource.PageSpec{Path: "accounts", Aliases: []string{"accounts"}, Handler: accountsPage(p, pool, []byte(csrfKey), cn.SessionCookieName())})
		p.MountPage(resource.PageSpec{Path: "password", Aliases: []string{"password"}, Handler: passwordPage(p, []byte(csrfKey), cn.SessionCookieName())})
		p.MountAction(resource.ActionSpec{Path: "password/change", Handler: passwordChange(p, acctStore, acctOf, []byte(csrfKey), cn.SessionCookieName(), accounts.NewLoginLimiter())})
		p.MountAction(resource.ActionSpec{Path: "accounts/{id}/activate", Handler: accountSetActive(acctStore, true)})
		p.MountAction(resource.ActionSpec{Path: "accounts/{id}/deactivate", Handler: accountSetActive(acctStore, false)})
	}
	mux.HandleFunc("GET "+adminBasePath+"/dashboard", a.Require(dashboardHandler(p, store, acctOf)))
	// POST action routes are mounted via p.MountAction, which wraps with the
	// auth guard, parses the form body, and verifies CSRF before calling Handler.
	p.MountAction(resource.ActionSpec{Path: "jobs/{id}/rate", Handler: rateHandler(store, acctOf)})
	p.MountAction(resource.ActionSpec{Path: "jobs/{id}/rescore", Handler: rescoreHandler(pool, store, acctOf)})
	p.MountAction(resource.ActionSpec{Path: "jobs/{id}/shortlist", Handler: shortlistHandler(store, acctOf)})
	// Inline pipeline-stage dropdown in the jobs table — note-preserving (SetStage, not Rate).
	p.MountAction(resource.ActionSpec{Path: "jobs/{id}/stage", Handler: stageHandler(store, acctOf)})
	// Detail-page triage form — triage-only (SetTriage); preserves stage + note.
	p.MountAction(resource.ActionSpec{Path: "jobs/{id}/triage", Handler: triageHandler(store, acctOf)})
	// Job posting lifecycle status dropdown on the detail page.
	p.MountAction(resource.ActionSpec{Path: "jobs/{id}/status", Handler: statusHandler(store)})
	mux.Handle("GET "+adminBasePath+"/jobs/{id}/download/{kind}", a.Require(downloadHandler(pool, authority, acctOf)))
	// /admin/shortlist (list + htmx rows) is handled by go-panel via resource.Register above.
	// shortlistDownloadHandler removed (orphaned route — Docs cell is a badge, not a link;
	// PDFs are accessible via the job detail page at /admin/jobs/{id}).
	mux.HandleFunc("GET "+adminBasePath+"/resume", a.Require(resumeHandler(p, acctOf)))
	// Resume editor routes (Part-D)
	mux.HandleFunc("GET "+adminBasePath+"/resume/edit", a.Require(resumeEditHandler(p, a, []byte(csrfKey), acctOf)))
	p.MountAction(resource.ActionSpec{Path: "resume/skill/{id}/level", Handler: resumeSkillLevelHandler(acctOf)})
	mux.HandleFunc("GET "+adminBasePath+"/linkedin", a.Require(linkedinHandler(p, authority.LegacyDir())))
	mux.HandleFunc("GET "+adminBasePath+"/upwork", a.Require(upworkHandler(p, a, []byte(csrfKey), acctOf)))
	p.MountAction(resource.ActionSpec{Path: "upwork/catalog/reorder", Handler: upworkCatalogReorderHandler(acctOf)})
	p.MountAction(resource.ActionSpec{Path: "upwork/skill/reorder", Handler: upworkSkillReorderHandler(acctOf)})
	p.MountAction(resource.ActionSpec{Path: "upwork/categories", Handler: upworkCategoriesEditHandler(acctOf)})
	// Wrap the go-panel catch-all with withSessionCookieContext so the
	// jobsLister closure can generate per-request CSRF tokens for the
	// star-toggle inline forms without needing the *http.Request.
	mux.Handle(adminBasePath+"/", withSessionCookieContext(cn.SessionCookieName(), p.Handler()))
	return mux, p, true
}

// checkAuthCapabilities panics at startup if a does not implement cookieNamer
// (SessionCookieName). Mirrors go-panel resource/resource.go:377 validateWriterConfig:
// the bespoke CSRF handlers on this mux perform the same session-cookie binding as
// go-panel's Writer path, so they need the same fail-closed guarantee at construction.
func checkAuthCapabilities(a auth.Authenticator) cookieNamer {
	cn, ok := any(a).(cookieNamer)
	if !ok {
		panic("adminui: authenticator must implement SessionCookieName() — CSRF session binding fail-closed")
	}
	return cn
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
