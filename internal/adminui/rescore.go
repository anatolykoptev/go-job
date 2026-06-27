package adminui

import (
	"context"
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

// jobScoreSetter is the narrow interface used to persist a ScoreResult.
// *hunt.Store satisfies this; tests inject a spy.
type jobScoreSetter interface {
	SetJobScore(ctx context.Context, id int64, sr hunt.ScoreResult) error
}

// rescoreJob executes the force-rescore action on an already-loaded job: calls
// score.ScoreForce and conditionally persists the result. Extracted for
// testability (no pool or HTTP request required).
//
// Guard — transient LLM fail-open:
// When the LLM was attempted but produced no valid score (LLMResult != ""),
// ScoreForce returns FitBand=unscored. Persisting that would CLOBBER a prior
// good "strong/85" analysis. The discriminator is:
//
//	result.FitBand == hunt.FitBandUnscored && result.LLMResult != ""
//
// On that path: log WARN, return (result, false, nil) — no write to the store.
// The handler redirects back to the detail page so the operator sees the
// unchanged (preserved) prior score.
//
// Normal paths:
//   - LLM returns a valid JSON → result has a real band → persisted.
//   - nil profile pre-guard → never reaches here (handler returns early).
func rescoreJob(
	ctx context.Context,
	id int64,
	job hunt.Job,
	prof *score.ScoringProfile,
	deps score.ScorerDeps,
	store jobScoreSetter,
) (hunt.ScoreResult, bool, error) {
	result := score.ScoreForce(ctx, prof, job, deps)

	// Guard: LLM was attempted but returned an error or un-parseable response
	// (cliproxyapi 503, transient proxy down, etc.). Do NOT clobber the existing
	// row — keep the prior analysis intact.
	if result.FitBand == hunt.FitBandUnscored && result.LLMResult != "" {
		slog.WarnContext(ctx, "force-rescore: LLM unavailable, prior score preserved",
			"job_id", id,
			"llm_result", result.LLMResult,
		)
		return result, false, nil
	}

	if err := store.SetJobScore(ctx, id, result); err != nil {
		return result, false, err
	}
	return result, true, nil
}

// rescoreHandler returns an http.HandlerFunc that force-scores a single job via
// the LLM, bypassing the recency and Jaccard pre-gates. Useful for stale or
// previously-rejected vacancies that the operator wants a real fit analysis on.
//
// Flow:
//  1. CSRF verification (mirrors rateHandler).
//  2. Load the job row from DB via scanJobDetail.
//  3. Load the current ScoringProfile.
//  4. Call rescoreJob (ScoreForce + guard + SetJobScore).
//  5. Redirect to the job detail page (303) — whether or not scoring succeeded.
//
// On profile-nil (scoring disabled): redirect without change, log WARN.
// On transient LLM fail-open: prior score preserved, operator redirected back.
//
// Wrap with a.Require() before mounting on the mux.
func rescoreHandler(pool *pgxpool.Pool, store jobScoreSetter, a auth.Authenticator, csrfKey []byte) http.HandlerFunc {
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
		sessVal := sessionValue(r, a.(cookieNamer).SessionCookieName())
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

		// Force-score with guard: transient LLM fail → prior score preserved, still redirect.
		_, _, persistErr := rescoreJob(ctx, id64, job, prof, deps, store)
		if persistErr != nil {
			slog.ErrorContext(ctx, "rescoreHandler: SetJobScore failed", "id", id64, "err", persistErr)
			http.Error(w, "persist failed", http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, fmt.Sprintf("/admin/jobs/%d", id64), http.StatusSeeOther)
	}
}
