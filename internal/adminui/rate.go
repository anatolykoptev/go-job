package adminui

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/anatolykoptev/go-panel/auth"
	"github.com/anatolykoptev/go-panel/csrf"
	"github.com/anatolykoptev/go_job/internal/hunt"
)

// validPipelineStages is the allowlist of accepted values for hunt_ratings.stage
// (pipeline axis). Derived from hunt.PipelineStages — single source of truth.
// Go-validated; no SQL CHECK constraint (ADR-go-job-003 addendum).
var validPipelineStages = func() map[string]bool {
	m := make(map[string]bool, len(hunt.PipelineStages))
	for _, s := range hunt.PipelineStages {
		m[s] = true
	}
	return m
}()

// validTriageStages is the allowlist of accepted values for hunt_ratings.triage
// (triage axis). Derived from hunt.TriageStages — single source of truth.
var validTriageStages = func() map[string]bool {
	m := make(map[string]bool, len(hunt.TriageStages))
	for _, s := range hunt.TriageStages {
		m[s] = true
	}
	return m
}()

// rateHandler returns an http.HandlerFunc that upserts the PIPELINE axis of a
// hunt_ratings row (hunt_ratings.stage + note). The triage axis is untouched
// (passed as "" to Store.Rate, which preserves the existing DB value).
// CSRF-protected. Wrap with a.Require() before mounting on the mux.
func rateHandler(store *hunt.Store, adminUser string, a auth.Authenticator, csrfKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rawID := r.PathValue("id")
		id64, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil || id64 <= 0 {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}

		const maxBody = 4096
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}

		// CSRF verification: token must be bound to the current session cookie.
		tok := r.FormValue(csrf.FormField)
		sessVal := sessionValue(r, a.(cookieNamer).SessionCookieName())
		if err := csrf.Verify(csrfKey, sessVal, tok); err != nil {
			http.Error(w, "invalid CSRF token", http.StatusForbidden)
			return
		}

		stage := r.FormValue("stage")
		note := r.FormValue("note")

		// Allow empty stage (clears pipeline position) but reject unknown non-empty values.
		if stage != "" && !validPipelineStages[stage] {
			http.Error(w, fmt.Sprintf("invalid pipeline stage %q", stage), http.StatusBadRequest)
			return
		}

		// Pass triage="" so Store.Rate preserves the existing triage value.
		if err := store.Rate(r.Context(), "job", id64, adminUser, "", stage, note); err != nil {
			slog.Error("rateHandler: upsert hunt_ratings", "id", id64, "err", err)
			http.Error(w, "update failed", http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, fmt.Sprintf("/admin/jobs/%d", id64), http.StatusSeeOther)
	}
}

// triageHandler returns an http.HandlerFunc that upserts ONLY the triage axis of a
// hunt_ratings row (hunt_ratings.triage). The pipeline stage and note are preserved
// (Store.SetTriage does not touch them). CSRF-protected.
// POSTs to /admin/jobs/{id}/triage. Wrap with a.Require() before mounting.
func triageHandler(store *hunt.Store, adminUser string, a auth.Authenticator, csrfKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rawID := r.PathValue("id")
		id64, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil || id64 <= 0 {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}

		const maxBody = 4096
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}

		tok := r.FormValue(csrf.FormField)
		sessVal := sessionValue(r, a.(cookieNamer).SessionCookieName())
		if err := csrf.Verify(csrfKey, sessVal, tok); err != nil {
			http.Error(w, "invalid CSRF token", http.StatusForbidden)
			return
		}

		triage := r.FormValue("triage")
		// Allow empty triage (clears interest signal) but reject unknown non-empty values.
		if triage != "" && !validTriageStages[triage] {
			http.Error(w, fmt.Sprintf("invalid triage %q", triage), http.StatusBadRequest)
			return
		}

		if err := store.SetTriage(r.Context(), "job", id64, adminUser, triage); err != nil {
			slog.Error("triageHandler: set triage", "id", id64, "err", err)
			http.Error(w, "update failed", http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, fmt.Sprintf("/admin/jobs/%d", id64), http.StatusSeeOther)
	}
}

// sessionValue reads the named cookie from the request and returns its Value,
// or "" if the cookie is absent.
func sessionValue(r *http.Request, cookieName string) string {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return ""
	}
	return c.Value
}
