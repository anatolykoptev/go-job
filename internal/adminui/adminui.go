// Package adminui serves go-job's operator admin (job/career tables) on a
// dedicated HTTP listener using the go-panel resource framework. It is
// fail-soft: New returns (nil,false) unless admin credentials are configured,
// so deploying before the env is wired changes nothing.
package adminui

import (
	"net/http"
	"os"
	"time"

	"github.com/anatolykoptev/go-panel/auth"
	"github.com/anatolykoptev/go-panel/resource"
	"github.com/anatolykoptev/go-panel/shell"
	"github.com/anatolykoptev/go_job/internal/engine/jobs/applications"
	"github.com/anatolykoptev/go_job/internal/hunt"
)

// adminBasePath is the base URL prefix for the admin panel.
// navIDLinkedin is the nav entry ID for the LinkedIn bespoke page (goconst: 4 occurrences).
const (
	adminBasePath = "/admin"
	navIDLinkedin = "linkedin"
)

// New builds the admin handler mounted at /admin. Returns (nil,false) when
// ADMIN_HMAC_KEY (>=32 bytes) or ADMIN_PASSWORD is unset — admin disabled.
//
// Routing: an outer http.ServeMux wraps p.Handler() as a /admin/ catch-all.
// Bespoke 4-/5-segment routes (POST /rate, GET /download/{kind}) precede the
// panel catch-all and do not shadow go-panel's 3-segment routes (/rows, /{id}).
// GET /admin/jobs/{id} is served by go-panel via the Detailer (natural URL).
func New(store *hunt.Store, authority *applications.Authority) (http.Handler, bool) {
	hmacKey := os.Getenv("ADMIN_HMAC_KEY")
	password := os.Getenv("ADMIN_PASSWORD")
	if len(hmacKey) < 32 || password == "" {
		return nil, false
	}
	csrfKey := os.Getenv("ADMIN_CSRF_KEY")
	if len(csrfKey) < 32 {
		csrfKey = hmacKey // single-operator fallback; HMAC key is already >=32 bytes
	}

	adminUser := envOr("ADMIN_USERNAME", "admin")

	a := auth.NewHMACAuth(auth.HMACConfig{
		Username:   adminUser,
		Password:   password,
		HMACKey:    []byte(hmacKey),
		BasePath:   adminBasePath,
		SessionTTL: 12 * time.Hour,
		Secure:     true,
	})
	checkAuthCapabilities(a)
	p := resource.New(resource.Config{
		Title:    "go-job",
		BasePath: adminBasePath,
		Auth:     a,
		CSRFKey:  []byte(csrfKey),
	})

	pool := store.Pool()

	// Shortlist (curated targets) is registered first so it appears first in the
	// Hunt nav group. resource.Register auto-routes /admin/shortlist and adds the
	// nav item — no manual p.AddNav call needed.
	resource.Register(p, shortlistResource(store, adminUser, authority, []byte(csrfKey)))

	// Wire Detailer onto the jobs resource so GET /admin/jobs/{id} is served
	// by go-panel's framework detail page instead of a bespoke handler.
	jr := jobsResource(store, adminUser, authority, []byte(csrfKey))
	jr.Detailer = jobDetailer(pool, store, adminUser, a, []byte(csrfKey), authority)
	resource.Register(p, jr)

	resource.Register(p, bountiesResource(pool))
	resource.Register(p, freelanceResource(pool))
	resource.Register(p, securityResource(pool))
	resource.Register(p, contestsResource(pool))
	resource.Register(p, oversizeResource(pool))

	// Sidebar nav entries for bespoke pages (appear below auto-generated resource items).
	p.AddNav(shell.NavItem{Group: grpHunt})
	p.AddNav(shell.NavItem{ID: navIDDashboard, Label: "Dashboard", URL: adminBasePath + "/dashboard"})
	p.AddNav(shell.NavItem{Group: "Profile"})
	p.AddNav(shell.NavItem{ID: "resume", Label: "Resume", Icon: "📄", URL: "/admin/resume"})
	p.AddNav(shell.NavItem{ID: navIDLinkedin, Label: "LinkedIn", Icon: "💼", URL: "/admin/linkedin"})
	p.AddNav(shell.NavItem{ID: navIDUpwork, Label: "Upwork", Icon: "🟢", URL: "/admin/upwork"})

	// Outer mux: bespoke 4-/5-segment routes first, panel catch-all last.
	// POST /rate and GET /download/{kind} are bespoke — not handled by Detailer.
	// GET /admin/jobs/{id} (natural 3-segment URL) is now served by go-panel.
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+adminBasePath+"/dashboard", a.Require(dashboardHandler(p, store, adminUser)))
	// POST action routes are mounted via p.MountAction, which wraps with the
	// auth guard, parses the form body, and verifies CSRF before calling Handler.
	p.MountAction(resource.ActionSpec{Path: "jobs/{id}/rate", Handler: rateHandler(store, adminUser)})
	p.MountAction(resource.ActionSpec{Path: "jobs/{id}/rescore", Handler: rescoreHandler(pool, store)})
	p.MountAction(resource.ActionSpec{Path: "jobs/{id}/shortlist", Handler: shortlistHandler(store, adminUser)})
	// Inline pipeline-stage dropdown in the jobs table — note-preserving (SetStage, not Rate).
	p.MountAction(resource.ActionSpec{Path: "jobs/{id}/stage", Handler: stageHandler(store, adminUser)})
	// Detail-page triage form — triage-only (SetTriage); preserves stage + note.
	p.MountAction(resource.ActionSpec{Path: "jobs/{id}/triage", Handler: triageHandler(store, adminUser)})
	// Job posting lifecycle status dropdown on the detail page.
	p.MountAction(resource.ActionSpec{Path: "jobs/{id}/status", Handler: statusHandler(store)})
	mux.Handle("GET "+adminBasePath+"/jobs/{id}/download/{kind}", a.Require(downloadHandler(pool, authority)))
	// /admin/shortlist (list + htmx rows) is handled by go-panel via resource.Register above.
	// shortlistDownloadHandler removed (orphaned route — Docs cell is a badge, not a link;
	// PDFs are accessible via the job detail page at /admin/jobs/{id}).
	mux.HandleFunc("GET "+adminBasePath+"/resume", a.Require(resumeHandler(p)))
	// Resume editor routes (Part-D)
	mux.HandleFunc("GET "+adminBasePath+"/resume/edit", a.Require(resumeEditHandler(p, a, []byte(csrfKey))))
	p.MountAction(resource.ActionSpec{Path: "resume/person", Handler: resumePersonEditHandler()})
	p.MountAction(resource.ActionSpec{Path: "resume/experience", Handler: resumeExperienceCreateHandler()})
	p.MountAction(resource.ActionSpec{Path: "resume/experience/{id}/delete", Handler: resumeExperienceDeleteHandler()})
	p.MountAction(resource.ActionSpec{Path: "resume/skill", Handler: resumeSkillCreateHandler()})
	p.MountAction(resource.ActionSpec{Path: "resume/skill/{id}/delete", Handler: resumeSkillDeleteHandler()})
	p.MountAction(resource.ActionSpec{Path: "resume/skill/{id}/level", Handler: resumeSkillLevelHandler()})
	p.MountAction(resource.ActionSpec{Path: "resume/achievement", Handler: resumeAchievementCreateHandler()})
	p.MountAction(resource.ActionSpec{Path: "resume/achievement/{id}/delete", Handler: resumeAchievementDeleteHandler()})
	p.MountAction(resource.ActionSpec{Path: "resume/domain", Handler: resumeDomainCreateHandler()})
	p.MountAction(resource.ActionSpec{Path: "resume/domain/{id}/delete", Handler: resumeDomainDeleteHandler()})
	p.MountAction(resource.ActionSpec{Path: "resume/methodology", Handler: resumeMethodologyCreateHandler()})
	p.MountAction(resource.ActionSpec{Path: "resume/methodology/{id}/delete", Handler: resumeMethodologyDeleteHandler()})
	p.MountAction(resource.ActionSpec{Path: "resume/project", Handler: resumeProjectCreateHandler()})
	p.MountAction(resource.ActionSpec{Path: "resume/project/{id}/delete", Handler: resumeProjectDeleteHandler()})
	p.MountAction(resource.ActionSpec{Path: "resume/education", Handler: resumeEducationCreateHandler()})
	p.MountAction(resource.ActionSpec{Path: "resume/education/{id}/delete", Handler: resumeEducationDeleteHandler()})
	p.MountAction(resource.ActionSpec{Path: "resume/certification", Handler: resumeCertificationCreateHandler()})
	p.MountAction(resource.ActionSpec{Path: "resume/certification/{id}/delete", Handler: resumeCertificationDeleteHandler()})
	mux.HandleFunc("GET "+adminBasePath+"/linkedin", a.Require(linkedinHandler(p, authority.LegacyDir())))
	mux.HandleFunc("GET "+adminBasePath+"/upwork", a.Require(upworkHandler(p, a, []byte(csrfKey))))
	p.MountAction(resource.ActionSpec{Path: "upwork/overview", Handler: upworkOverviewEditHandler()})
	p.MountAction(resource.ActionSpec{Path: "upwork/skill", Handler: upworkSkillCreateHandler()})
	p.MountAction(resource.ActionSpec{Path: "upwork/skill/{id}/delete", Handler: upworkSkillDeleteHandler()})
	p.MountAction(resource.ActionSpec{Path: "upwork/catalog", Handler: upworkCatalogCreateHandler()})
	p.MountAction(resource.ActionSpec{Path: "upwork/catalog/{id}/delete", Handler: upworkCatalogDeleteHandler()})
	p.MountAction(resource.ActionSpec{Path: "upwork/catalog/reorder", Handler: upworkCatalogReorderHandler()})
	p.MountAction(resource.ActionSpec{Path: "upwork/skill/reorder", Handler: upworkSkillReorderHandler()})
	p.MountAction(resource.ActionSpec{Path: "upwork/categories", Handler: upworkCategoriesEditHandler()})
	// Wrap the go-panel catch-all with withSessionCookieContext so the
	// jobsLister closure can generate per-request CSRF tokens for the
	// star-toggle inline forms without needing the *http.Request.
	mux.Handle(adminBasePath+"/", withSessionCookieContext(a.SessionCookieName(), p.Handler()))
	return mux, true
}

// checkAuthCapabilities panics at startup if a does not implement cookieNamer
// (SessionCookieName). Mirrors go-panel resource/resource.go:377 validateWriterConfig:
// the bespoke CSRF handlers on this mux perform the same session-cookie binding as
// go-panel's Writer path, so they need the same fail-closed guarantee at construction.
func checkAuthCapabilities(a auth.Authenticator) {
	if _, ok := any(a).(cookieNamer); !ok {
		panic("adminui: authenticator must implement SessionCookieName() — CSRF session binding fail-closed")
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
