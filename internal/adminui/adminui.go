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

// New builds the admin handler mounted at /admin. Returns (nil,false) when
// ADMIN_HMAC_KEY (>=32 bytes) or ADMIN_PASSWORD is unset — admin disabled.
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
		BasePath:   "/admin",
		SessionTTL: 12 * time.Hour,
		Secure:     true,
	})
	p := resource.New(resource.Config{
		Title:    "go-job",
		BasePath: "/admin",
		Auth:     a,
		CSRFKey:  []byte(csrfKey),
	})
	resource.Register(p, jobsResource(pool))
	resource.Register(p, bountiesResource(pool))
	resource.Register(p, freelanceResource(pool))
	resource.Register(p, securityResource(pool))
	resource.Register(p, contestsResource(pool))
	return p.Handler(), true
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
