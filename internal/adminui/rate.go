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

// validHuntStages is the allowlist of accepted stage values for hunt_ratings.
var validHuntStages = map[string]bool{
	"new":         true,
	"interesting": true,
	"saved":       true,
	"discarded":   true,
	"claimed":     true,
}

// rateHandler returns an http.HandlerFunc that upserts a hunt_ratings row.
// The handler verifies the CSRF token before writing.
// Wrap with a.Require() before mounting on the mux.
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
		if !validHuntStages[stage] {
			http.Error(w, fmt.Sprintf("invalid stage %q", stage), http.StatusBadRequest)
			return
		}
		note := r.FormValue("note")

		if err := store.Rate(r.Context(), "job", id64, adminUser, stage, note); err != nil {
			slog.Error("rateHandler: upsert hunt_ratings", "id", id64, "err", err)
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
