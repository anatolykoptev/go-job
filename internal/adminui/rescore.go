package adminui

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/anatolykoptev/go-panel/auth"
	"github.com/anatolykoptev/go-panel/csrf"
	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/anatolykoptev/go_job/internal/hunt/score"
	"github.com/jackc/pgx/v5/pgxpool"
)

// rescoreHandler returns an http.HandlerFunc that force-scores a single job via
// the LLM, bypassing the recency and Jaccard pre-gates. Useful for stale or
// previously-rejected vacancies that the operator wants a real fit analysis on.
//
// Flow:
//  1. CSRF verification (mirrors rateHandler).
//  2. Load the job row from DB via scanJobDetail.
//  3. Load the current ScoringProfile.
//  4. Call score.ScoreForce — bypasses recency + Jaccard, runs LLM directly.
//  5. Persist via store.SetJobScore (same SQL path as huntworker).
//  6. Redirect to the job detail page (303).
//
// On profile-nil (scoring disabled): redirect without change, log WARN.
// On LLM or parse error: score.ScoreForce uses fail-open semantics; the
// result is persisted as unscored and the redirect still fires.
//
// Wrap with a.Require() before mounting on the mux.
func rescoreHandler(pool *pgxpool.Pool, store *hunt.Store, a *auth.HMACAuth, csrfKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rawID := r.PathValue("id")
		id64, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil || id64 <= 0 {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}

		const maxBody = 512
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}

		// CSRF verification: token must be bound to the current session cookie.
		tok := r.FormValue(csrf.FormField)
		sessVal := sessionValue(r, a.SessionCookieName())
		if err := csrf.Verify(csrfKey, sessVal, tok); err != nil {
			http.Error(w, "invalid CSRF token", http.StatusForbidden)
			return
		}

		ctx := r.Context()

		// Load the job row using the same query as the detail page.
		rec, loadErr := scanJobDetail(ctx, pool, id64)
		if loadErr != nil {
			if isJobNotFound(loadErr) {
				http.Error(w, "job not found", http.StatusNotFound)
				return
			}
			slog.ErrorContext(ctx, "rescoreHandler: scan job", "id", id64, "err", loadErr)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		// Build a hunt.Job with the fields the scorer needs.
		// SalaryMin/Max are *int in the record; coerce nil → 0 for hunt.Job.
		salaryMin := 0
		if rec.SalaryMin != nil {
			salaryMin = *rec.SalaryMin
		}
		salaryMax := 0
		if rec.SalaryMax != nil {
			salaryMax = *rec.SalaryMax
		}
		job := hunt.Job{
			ID:             id64,
			Title:          rec.Title,
			Company:        rec.Company,
			Location:       rec.Location,
			SalaryMin:      salaryMin,
			SalaryMax:      salaryMax,
			SalaryCurrency: rec.SalaryCurrency,
			SalaryInterval: rec.SalaryInterval,
			Description:    rec.DescRaw,
			PostedAt:       rec.PostedAt,
		}

		// Load scoring profile (same call as huntworker startup).
		prof, profErr := score.LoadProfile(ctx, pool)
		if profErr != nil {
			slog.WarnContext(ctx, "rescoreHandler: load profile failed", "id", id64, "err", profErr)
			http.Error(w, "profile load error", http.StatusInternalServerError)
			return
		}
		if prof == nil {
			// Scoring is disabled (no profile). Redirect without change.
			slog.WarnContext(ctx, "rescoreHandler: scoring profile nil — rescore skipped", "id", id64)
			http.Redirect(w, r, fmt.Sprintf("/admin/jobs/%d", id64), http.StatusSeeOther)
			return
		}

		// Build ScorerDeps identical to huntworker (Jaccard wired but not called by ScoreForce).
		deps := score.ScorerDeps{
			Jaccard: func(profileKW, jobText string) float64 {
				kw := jobs.ExtractResumeKeywords(profileKW)
				return jobs.ScoreJobMatchCoverage(kw, jobText)
			},
			LLM: engine.CallLLM,
		}

		// Force-score: bypasses recency + Jaccard, goes straight to the LLM.
		result := score.ScoreForce(ctx, prof, job, deps)

		// Persist via the same store path used by huntworker.
		if err := store.SetJobScore(ctx, id64, result); err != nil {
			slog.ErrorContext(ctx, "rescoreHandler: SetJobScore failed", "id", id64, "err", err)
			http.Error(w, "persist failed", http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, fmt.Sprintf("/admin/jobs/%d", id64), http.StatusSeeOther)
	}
}
