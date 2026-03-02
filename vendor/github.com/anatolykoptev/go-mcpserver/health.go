package mcpserver

import (
	"fmt"
	"net/http"
)

// registerHealth adds /health, /health/live, and /health/ready endpoints.
// If cfg.DisableHealth is true, no endpoints are registered.
func registerHealth(mux *http.ServeMux, cfg Config) {
	if cfg.DisableHealth {
		return
	}

	healthBody := `{"status":"ok","service":"` + cfg.Name + `","version":"` + cfg.Version + `"}`

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(healthBody))
	})

	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, _ *http.Request) {
		if cfg.ReadinessCheck != nil {
			if err := cfg.ReadinessCheck(); err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				fmt.Fprintf(w, `{"status":"unavailable","error":%q}`, err.Error())
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
}
