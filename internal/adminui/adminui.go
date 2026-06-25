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
	"github.com/jackc/pgx/v5/pgxpool"
)

// adminBasePath is the base URL prefix for the admin panel.
const adminBasePath = "/admin"

// New builds the admin handler mounted at /admin. Returns (nil,false) when
// ADMIN_HMAC_KEY (>=32 bytes) or ADMIN_PASSWORD is unset — admin disabled.
//
// Routing: an outer http.ServeMux wraps p.Handler() as a /admin/ catch-all,
// allowing bespoke 4-segment routes (e.g. GET /admin/jobs/{id}/view) that
// never collide with go-panel's 3-segment routes (GET /admin/jobs/rows, etc.).
func New(pool *pgxpool.Pool) (http.Handler, bool) {
	hmacKey := os.Getenv("ADMIN_HMAC_KEY")
	password := os.Getenv("ADMIN_PASSWORD")
	if len(hmacKey) < 32 || password == "" {
		return nil, false
	}
	csrfKey := os.Getenv("ADMIN_CSRF_KEY")
	if len(csrfKey) < 32 {
		csrfKey = hmacKey // single-operator fallback; HMAC key is already >=32 bytes
	}

	a := auth.NewHMACAuth(auth.HMACConfig{
		Username:   envOr("ADMIN_USERNAME", "admin"),
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
	resource.Register(p, jobsResource(pool))
	resource.Register(p, bountiesResource(pool))
	resource.Register(p, freelanceResource(pool))
	resource.Register(p, securityResource(pool))
	resource.Register(p, contestsResource(pool))
	resource.Register(p, oversizeResource(pool))

	applicationsDir := envOr("APPLICATIONS_DIR", "/data/applications")

	// Outer mux: bespoke routes (4-/5-segment) first, panel catch-all last.
	// These routes cannot shadow go-panel's 3-segment routes (/rows, /new, /{id}).
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+adminBasePath+"/jobs/{id}/view", a.Require(jobDetailHandler(pool, a, []byte(csrfKey))))
	mux.Handle("POST "+adminBasePath+"/jobs/{id}/rate", a.Require(rateHandler(pool, a, []byte(csrfKey))))
	mux.Handle("GET "+adminBasePath+"/jobs/{id}/download/{kind}", a.Require(downloadHandler(pool, applicationsDir)))
	mux.Handle(adminBasePath+"/", p.Handler())
	return mux, true
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
