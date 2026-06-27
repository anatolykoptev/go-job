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
	resource.Register(p, shortlistResource(store, adminUser, authority))

	// Wire Detailer onto the jobs resource so GET /admin/jobs/{id} is served
	// by go-panel's framework detail page instead of a bespoke handler.
	jr := jobsResource(pool)
	jr.Detailer = jobDetailer(pool, store, adminUser, a, []byte(csrfKey), authority)
	resource.Register(p, jr)

	resource.Register(p, bountiesResource(pool))
	resource.Register(p, freelanceResource(pool))
	resource.Register(p, securityResource(pool))
	resource.Register(p, contestsResource(pool))
	resource.Register(p, oversizeResource(pool))

	// Sidebar nav entries for bespoke pages (appear below auto-generated resource items).
	p.AddNav(shell.NavItem{Group: "Profile"})
	p.AddNav(shell.NavItem{ID: "resume", Label: "Resume", Icon: "📄", URL: "/admin/resume"})
	p.AddNav(shell.NavItem{ID: navIDLinkedin, Label: "LinkedIn", Icon: "💼", URL: "/admin/linkedin"})
	p.AddNav(shell.NavItem{ID: navIDUpwork, Label: "Upwork", Icon: "🟢", URL: "/admin/upwork"})

	// Outer mux: bespoke 4-/5-segment routes first, panel catch-all last.
	// POST /rate and GET /download/{kind} are bespoke — not handled by Detailer.
	// GET /admin/jobs/{id} (natural 3-segment URL) is now served by go-panel.
	mux := http.NewServeMux()
	mux.Handle("POST "+adminBasePath+"/jobs/{id}/rate", a.Require(rateHandler(store, adminUser, a, []byte(csrfKey))))
	mux.Handle("POST "+adminBasePath+"/jobs/{id}/rescore", a.Require(rescoreHandler(pool, store, a, []byte(csrfKey))))
	mux.Handle("GET "+adminBasePath+"/jobs/{id}/download/{kind}", a.Require(downloadHandler(pool, authority)))
	// /admin/shortlist (list + htmx rows) is handled by go-panel via resource.Register above.
	// shortlistDownloadHandler removed (orphaned route — Docs cell is a badge, not a link;
	// PDFs are accessible via the job detail page at /admin/jobs/{id}).
	mux.HandleFunc("GET "+adminBasePath+"/resume", a.Require(resumeHandler(p)))
	// Resume editor routes (Part-D)
	mux.HandleFunc("GET "+adminBasePath+"/resume/edit", a.Require(resumeEditHandler(p, a, []byte(csrfKey))))
	mux.Handle("POST "+adminBasePath+"/resume/person", a.Require(resumePersonEditHandler(a, []byte(csrfKey))))
	mux.Handle("POST "+adminBasePath+"/resume/experience", a.Require(resumeExperienceCreateHandler(a, []byte(csrfKey))))
	mux.Handle("POST "+adminBasePath+"/resume/experience/{id}/delete", a.Require(resumeExperienceDeleteHandler(a, []byte(csrfKey))))
	mux.Handle("POST "+adminBasePath+"/resume/skill", a.Require(resumeSkillCreateHandler(a, []byte(csrfKey))))
	mux.Handle("POST "+adminBasePath+"/resume/skill/{id}/delete", a.Require(resumeSkillDeleteHandler(a, []byte(csrfKey))))
	mux.Handle("POST "+adminBasePath+"/resume/skill/{id}/level", a.Require(resumeSkillLevelHandler(a, []byte(csrfKey))))
	mux.Handle("POST "+adminBasePath+"/resume/achievement", a.Require(resumeAchievementCreateHandler(a, []byte(csrfKey))))
	mux.Handle("POST "+adminBasePath+"/resume/achievement/{id}/delete", a.Require(resumeAchievementDeleteHandler(a, []byte(csrfKey))))
	mux.Handle("POST "+adminBasePath+"/resume/domain", a.Require(resumeDomainCreateHandler(a, []byte(csrfKey))))
	mux.Handle("POST "+adminBasePath+"/resume/domain/{id}/delete", a.Require(resumeDomainDeleteHandler(a, []byte(csrfKey))))
	mux.Handle("POST "+adminBasePath+"/resume/methodology", a.Require(resumeMethodologyCreateHandler(a, []byte(csrfKey))))
	mux.Handle("POST "+adminBasePath+"/resume/methodology/{id}/delete", a.Require(resumeMethodologyDeleteHandler(a, []byte(csrfKey))))
	mux.Handle("POST "+adminBasePath+"/resume/project", a.Require(resumeProjectCreateHandler(a, []byte(csrfKey))))
	mux.Handle("POST "+adminBasePath+"/resume/project/{id}/delete", a.Require(resumeProjectDeleteHandler(a, []byte(csrfKey))))
	mux.Handle("POST "+adminBasePath+"/resume/education", a.Require(resumeEducationCreateHandler(a, []byte(csrfKey))))
	mux.Handle("POST "+adminBasePath+"/resume/education/{id}/delete", a.Require(resumeEducationDeleteHandler(a, []byte(csrfKey))))
	mux.Handle("POST "+adminBasePath+"/resume/certification", a.Require(resumeCertificationCreateHandler(a, []byte(csrfKey))))
	mux.Handle("POST "+adminBasePath+"/resume/certification/{id}/delete", a.Require(resumeCertificationDeleteHandler(a, []byte(csrfKey))))
	mux.HandleFunc("GET "+adminBasePath+"/linkedin", a.Require(linkedinHandler(p, authority.LegacyDir())))
	mux.HandleFunc("GET "+adminBasePath+"/upwork", a.Require(upworkHandler(p, a, []byte(csrfKey))))
	mux.Handle("POST "+adminBasePath+"/upwork/overview", a.Require(upworkOverviewEditHandler(a, []byte(csrfKey))))
	mux.Handle("POST "+adminBasePath+"/upwork/skill", a.Require(upworkSkillCreateHandler(a, []byte(csrfKey))))
	mux.Handle("POST "+adminBasePath+"/upwork/skill/{id}/delete", a.Require(upworkSkillDeleteHandler(a, []byte(csrfKey))))
	mux.Handle("POST "+adminBasePath+"/upwork/catalog", a.Require(upworkCatalogCreateHandler(a, []byte(csrfKey))))
	mux.Handle("POST "+adminBasePath+"/upwork/catalog/{id}/delete", a.Require(upworkCatalogDeleteHandler(a, []byte(csrfKey))))
	mux.Handle("POST "+adminBasePath+"/upwork/catalog/reorder", a.Require(upworkCatalogReorderHandler(a, []byte(csrfKey))))
	mux.Handle("POST "+adminBasePath+"/upwork/skill/reorder", a.Require(upworkSkillReorderHandler(a, []byte(csrfKey))))
	mux.Handle("POST "+adminBasePath+"/upwork/categories", a.Require(upworkCategoriesEditHandler(a, []byte(csrfKey))))
	mux.Handle(adminBasePath+"/", p.Handler())
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
