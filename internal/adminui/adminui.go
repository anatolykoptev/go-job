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
	"github.com/anatolykoptev/go_job/internal/hunt"
)

// adminBasePath is the base URL prefix for the admin panel.
const adminBasePath = "/admin"

// New builds the admin handler mounted at /admin. Returns (nil,false) when
// ADMIN_HMAC_KEY (>=32 bytes) or ADMIN_PASSWORD is unset — admin disabled.
//
// Routing: an outer http.ServeMux wraps p.Handler() as a /admin/ catch-all,
// allowing bespoke 4-segment routes (e.g. GET /admin/jobs/{id}/view) that
// never collide with go-panel's 3-segment routes (GET /admin/jobs/rows, etc.).
func New(store *hunt.Store) (http.Handler, bool) {
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
	p := resource.New(resource.Config{
		Title:    "go-job",
		BasePath: adminBasePath,
		Auth:     a,
		CSRFKey:  []byte(csrfKey),
	})

	pool := store.Pool()
	resource.Register(p, jobsResource(pool))
	resource.Register(p, bountiesResource(pool))
	resource.Register(p, freelanceResource(pool))
	resource.Register(p, securityResource(pool))
	resource.Register(p, contestsResource(pool))
	resource.Register(p, oversizeResource(pool))

	// Sidebar nav entries for bespoke pages (appear below auto-generated resource items).
	p.AddNav(shell.NavItem{Group: "Profile"})
	p.AddNav(shell.NavItem{ID: "resume", Label: "Resume", Icon: "📄", URL: "/admin/resume"})
	p.AddNav(shell.NavItem{ID: "linkedin", Label: "LinkedIn", Icon: "💼", URL: "/admin/linkedin"})

	applicationsDir := envOr("APPLICATIONS_DIR", "/data/applications")

	// Outer mux: bespoke routes (4-/5-segment) first, panel catch-all last.
	// These routes cannot shadow go-panel's 3-segment routes (/rows, /new, /{id}).
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+adminBasePath+"/jobs/{id}/view", a.Require(jobDetailHandler(p, store, adminUser, a, []byte(csrfKey))))
	mux.Handle("POST "+adminBasePath+"/jobs/{id}/rate", a.Require(rateHandler(store, adminUser, a, []byte(csrfKey))))
	mux.Handle("GET "+adminBasePath+"/jobs/{id}/download/{kind}", a.Require(downloadHandler(pool, applicationsDir)))
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
	mux.HandleFunc("GET "+adminBasePath+"/linkedin", a.Require(linkedinHandler(p, applicationsDir)))
	mux.Handle(adminBasePath+"/", p.Handler())
	return mux, true
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
