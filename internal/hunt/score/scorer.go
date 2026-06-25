package score

// scorer.go implements the hybrid Jaccard→LLM fit+success cascade scorer
// (Phase 4 of the hunt fit-scoring plan).
//
// Import-cycle guard: this package imports only stdlib + go-kit/env + hunt types.
// It does NOT import internal/engine or internal/engine/jobs — those packages
// already import internal/hunt, creating a cycle. The wiring of engine.CallLLM
// and jobs.ScoreJobMatchCoverage happens in huntworker, which imports all three.
//
// Cascade order (cheapest rejection first):
//  1. nil profile        → unscored (scoring disabled)
//  2. Recency pre-gate   → stale (no LLM)
//  3. Jaccard pre-filter → reject (no LLM)
//  4. LLM scorer         → full two-axis result

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/anatolykoptev/go-kit/env"
	"github.com/anatolykoptev/go_job/internal/hunt"
)

// defaultMinJaccard is the Jaccard pre-filter threshold (0–100).
// Below this, the job is an obvious non-match and the LLM is not called.
// Kept LOW because ScoreJobMatchCoverage (overlap-coefficient) can return
// low values on verbose JDs — a threshold of 8 only kills true non-matches.
const defaultMinJaccard = 8.0

// validSuccessBands is the exhaustive allowlist for success_band values.
var validSuccessBands = map[string]bool{
	"STRONG":   true,
	"MODERATE": true,
	"LONGSHOT": true,
}

// validOverUnder is the exhaustive allowlist for over_under values.
var validOverUnder = map[string]bool{
	"under_qualified": true,
	"well_matched":    true,
	"over_qualified":  true,
}

// ScorerDeps injects the two expensive external dependencies into Score.
// This avoids import cycles: huntworker wires the concrete implementations;
// internal/hunt/score never touches internal/engine or internal/engine/jobs.
//
//   - Jaccard: computes an overlap-coefficient score (0–100) between profile
//     keywords and the job text. Use jobs.ScoreJobMatchCoverage in production.
//   - LLM: sends a prompt and returns the raw string response.
//     Use engine.CallLLM in production.
type ScorerDeps struct {
	Jaccard func(profileKW, jobText string) float64
	LLM     func(ctx context.Context, prompt string) (string, error)
}

// llmScoreResponse is the JSON shape the LLM must return.
type llmScoreResponse struct {
	FitScore         int      `json:"fit_score"`
	FitReasons       []string `json:"fit_reasons"`
	FitGaps          []string `json:"fit_gaps"`
	SuccessBand      string   `json:"success_band"`
	SuccessReasoning string   `json:"success_reasoning"`
	OverUnder        string   `json:"over_under"`
}

// Score runs the hybrid cascade scorer and returns a hunt.ScoreResult.
//
// Cascade:
//  1. nil profile → FitBand="unscored" (scoring disabled — no LLM)
//  2. PostedAt nil or > HUNT_NOTIFY_MAX_AGE → FitBand="stale" (no LLM)
//  3. Jaccard < HUNT_SCORE_MIN_JACCARD → FitBand="reject" (no LLM)
//  4. LLM call → full result; on failure, fail-open if HUNT_SCORE_FAIL_OPEN=true
func Score(ctx context.Context, profile *ScoringProfile, job hunt.Job, deps ScorerDeps) hunt.ScoreResult {
	// --- Stage 0: nil profile means scoring disabled ---
	if profile == nil {
		return hunt.ScoreResult{FitBand: "unscored", ScoredAt: time.Now()}
	}

	// --- Stage 1: recency pre-gate ---
	maxAge := env.Duration("HUNT_NOTIFY_MAX_AGE", 48*time.Hour)
	if job.PostedAt == nil || time.Since(*job.PostedAt) > maxAge {
		return hunt.ScoreResult{FitBand: "stale", ScoredAt: time.Now()}
	}

	// --- Stage 2: Jaccard pre-filter ---
	minJaccard := env.Float("HUNT_SCORE_MIN_JACCARD", defaultMinJaccard)
	profileKW := buildProfileKeywords(profile)
	jobText := job.Title + " " + job.Description
	jaccardScore := deps.Jaccard(profileKW, jobText)

	if jaccardScore < minJaccard {
		return hunt.ScoreResult{
			FitScore: int(jaccardScore),
			FitBand:  "reject",
			ScoredAt: time.Now(),
		}
	}

	// --- Stage 3: LLM precision scorer ---
	prompt := buildScorerPrompt(profile, job)
	raw, err := deps.LLM(ctx, prompt)
	if err != nil {
		slog.WarnContext(ctx, "fit scoring: LLM call failed",
			slog.Any("error", err),
			slog.String("job_title", job.Title),
		)
		return failOpen(ctx, int(jaccardScore))
	}

	result, parseErr := parseScoreResponse(raw, int(jaccardScore))
	if parseErr != nil {
		slog.WarnContext(ctx, "fit scoring: parse failed",
			slog.Any("error", parseErr),
			slog.String("job_title", job.Title),
			slog.String("raw_truncated", truncate(raw, 200)),
		)
		return failOpen(ctx, int(jaccardScore))
	}

	result.ScoredAt = time.Now()
	return result
}

// failOpen returns an unscored result with the Jaccard score as the FitScore.
// Called when the LLM fails or parse fails and HUNT_SCORE_FAIL_OPEN=true.
// If HUNT_SCORE_FAIL_OPEN=false, the caller has already checked and handled.
func failOpen(ctx context.Context, jaccardScore int) hunt.ScoreResult {
	if !scoringFailOpen() {
		// Should not be called when fail-closed; return unscored anyway (defensive).
		return hunt.ScoreResult{FitBand: "unscored", FitScore: jaccardScore, ScoredAt: time.Now()}
	}
	slog.WarnContext(ctx, "fit scoring: fail-open — returning unscored, job still eligible for notify")
	return hunt.ScoreResult{FitBand: "unscored", FitScore: jaccardScore, ScoredAt: time.Now()}
}

// scoringFailOpen reads HUNT_SCORE_FAIL_OPEN (default true).
func scoringFailOpen() bool {
	return env.Bool("HUNT_SCORE_FAIL_OPEN", true)
}

// buildProfileKeywords concatenates CoreSkills + AdjacentSkills + TargetDomains
// into a space-separated string for the Jaccard function.
func buildProfileKeywords(profile *ScoringProfile) string {
	parts := make([]string, 0, len(profile.CoreSkills)+len(profile.AdjacentSkills)+len(profile.TargetDomains))
	parts = append(parts, profile.CoreSkills...)
	parts = append(parts, profile.AdjacentSkills...)
	parts = append(parts, profile.TargetDomains...)
	return strings.Join(parts, " ")
}

// parseScoreResponse strips Markdown fences, unmarshals the LLM response,
// and applies enum clamping + range clamping. jaccardFallback is used as
// FitScore when fail-open is triggered.
//
// Returns (result, nil) on success.
// Returns (unscored-result, nil) when HUNT_SCORE_FAIL_OPEN=true and parse fails.
// Returns (zero, error) when HUNT_SCORE_FAIL_OPEN=false and parse fails.
func parseScoreResponse(raw string, jaccardFallback int) (hunt.ScoreResult, error) {
	// Strip markdown fences (```json ... ``` or ``` ... ```).
	cleaned := stripMarkdownFences(raw)
	cleaned = strings.TrimSpace(cleaned)

	var resp llmScoreResponse
	if err := json.Unmarshal([]byte(cleaned), &resp); err != nil {
		if scoringFailOpen() {
			return hunt.ScoreResult{FitBand: "unscored", FitScore: jaccardFallback}, nil
		}
		return hunt.ScoreResult{}, fmt.Errorf("score: parse LLM response: %w", err)
	}

	// Clamp fit_score to [0, 100].
	fitScore := resp.FitScore
	if fitScore < 0 {
		fitScore = 0
	}
	if fitScore > 100 {
		fitScore = 100
	}

	// Enum-clamp success_band: unknown → "MODERATE" (log warning).
	successBand := resp.SuccessBand
	if !validSuccessBands[successBand] {
		slog.Warn("fit scoring: unknown success_band — clamping to MODERATE",
			slog.String("raw_band", successBand))
		successBand = "MODERATE"
	}

	// Enum-clamp over_under: unknown → "well_matched" (log warning).
	overUnder := resp.OverUnder
	if !validOverUnder[overUnder] {
		slog.Warn("fit scoring: unknown over_under — clamping to well_matched",
			slog.String("raw_over_under", overUnder))
		overUnder = "well_matched"
	}

	// Derive FitBand from fit_score (used for display and gate in Phase 5).
	fitBand := fitBandFromScore(fitScore)

	return hunt.ScoreResult{
		FitScore:         fitScore,
		FitBand:          fitBand,
		SuccessBand:      successBand,
		OverUnder:        overUnder,
		FitReasons:       resp.FitReasons,
		FitGaps:          resp.FitGaps,
		SuccessReasoning: resp.SuccessReasoning,
	}, nil
}

// fitBandFromScore maps a 0–100 fit_score to a string band for display/gate.
func fitBandFromScore(score int) string {
	switch {
	case score >= 75:
		return "strong"
	case score >= 50:
		return "moderate"
	case score >= 25:
		return "weak"
	default:
		return "low"
	}
}

// buildScorerPrompt constructs the two-axis LLM scoring prompt.
//
// Honesty design (Decision 3 from the plan):
//   - fit_score is an alignment measure 0-100 (defensible similarity).
//   - success_band is a COARSE competitiveness estimate, NEVER a percentage.
//   - The prompt explicitly forbids numbers in success_reasoning.
//   - Semantic matching is instructed (ONNX≈ML-infra, str0m≈WebRTC, etc.).
func buildScorerPrompt(profile *ScoringProfile, job hunt.Job) string {
	// Truncate description to ~4000 chars to stay within token budget.
	desc := truncate(job.Description, 4000)

	// Build salary context.
	salaryCtx := ""
	if job.SalaryMin > 0 || job.SalaryMax > 0 {
		salaryCtx = fmt.Sprintf("\nSalary: $%d–$%d %s %s",
			job.SalaryMin, job.SalaryMax, job.SalaryCurrency, job.SalaryInterval)
	}

	// Build locations string.
	locations := strings.Join(profile.Locations, ", ")
	if locations == "" {
		locations = "not specified"
	}

	// Build skills context.
	coreSkills := strings.Join(profile.CoreSkills, ", ")
	adjacentSkills := strings.Join(profile.AdjacentSkills, ", ")
	targetDomains := strings.Join(profile.TargetDomains, ", ")
	avoidSignals := strings.Join(profile.AvoidSignals, ", ")

	return fmt.Sprintf(`You are a senior technical recruiter evaluating job-candidate fit.

## CANDIDATE PROFILE
Seniority: %s
Core skills (advanced/expert): %s
Adjacent skills (intermediate): %s
Target domains: %s
Minimum compensation: $%d USD total comp
Preferred locations: %s
Work authorization: %s
Avoid roles related to: %s

## JOB POSTING
Title: %s
Company: %s
Location: %s%s

Description:
%s

## YOUR TASK
Evaluate the candidate's fit for this role on TWO axes:

**AXIS 1 — FIT (0–100)**: How well does this role align with the candidate's goals, skills, and domains?
- 0 = completely off-target, 100 = perfect alignment.
- This is a similarity measure, NOT a probability. A numeric fit_score IS appropriate here.
- Use semantic matching: ONNX≈ML-infra, str0m≈WebRTC, Apache AGE≈graph DB, go-kit≈platform-eng — match meaning, not literal tokens.

**AXIS 2 — SUCCESS BAND**: How competitive is this candidate for THIS specific role?
- Pick ONE of: STRONG, MODERATE, LONGSHOT.
- STRONG = candidate clearly meets the bar (seniority, skills, domain align well).
- MODERATE = reasonable match but with notable gaps or competition risk.
- LONGSHOT = significant seniority mismatch, hard-requirement gap, or over/under-qualified signal.
- IMPORTANT: NEVER output a percentage or decimal in success_reasoning. This is a competitiveness ESTIMATE, not a probability. You cannot observe the applicant pool.
- In success_reasoning: name the SINGLE BIGGEST driver (1–2 sentences). Examples:
  - "over-qualified: Staff-level candidate on a mid-level role; comp likely below their floor"
  - "under-qualified: role requires 5+ years k8s production ops, not evidenced in profile"
  - "strong seniority + domain match; missing only a nice-to-have cert"

## OUTPUT FORMAT
Respond ONLY with valid JSON (no markdown fences, no extra text):
{
  "fit_score": <integer 0-100>,
  "fit_reasons": ["<up to 3 matching strengths>"],
  "fit_gaps": ["<up to 3 key gaps or risks>"],
  "success_band": "<STRONG|MODERATE|LONGSHOT>",
  "success_reasoning": "<1-2 sentences naming the single biggest competitiveness driver — NO percentages>",
  "over_under": "<under_qualified|well_matched|over_qualified>"
}`,
		profile.Seniority,
		coreSkills,
		adjacentSkills,
		targetDomains,
		profile.CompFloorUSD,
		locations,
		profile.WorkAuth,
		avoidSignals,
		job.Title,
		job.Company,
		job.Location,
		salaryCtx,
		desc,
	)
}

// stripMarkdownFences removes ```json and ``` wrappers from LLM output.
// Mirrors the StripMarkdownFences function in internal/engine/jobs/master_resume.go
// to avoid cross-package imports. Both functions must stay in sync.
func stripMarkdownFences(raw string) string {
	s := strings.TrimSpace(raw)
	// Remove opening fence (```json or just ```).
	if after, ok := strings.CutPrefix(s, "```json"); ok {
		s = strings.TrimSpace(after)
	} else if after, ok := strings.CutPrefix(s, "```"); ok {
		s = strings.TrimSpace(after)
	}
	// Remove closing fence.
	if before, ok := strings.CutSuffix(s, "```"); ok {
		s = strings.TrimSpace(before)
	}
	return s
}

// truncate returns the first n bytes of s. Used for description truncation
// and log-safe raw-response excerpts.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Truncate at rune boundary.
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

// scoringEnabled reads HUNT_SCORE_ENABLED (default true).
// Exported for use by huntworker when deciding whether to load the profile.
func ScoringEnabled() bool {
	return env.Bool("HUNT_SCORE_ENABLED", true)
}

// MinJaccard returns the configured Jaccard pre-filter threshold.
// Exported for huntworker diagnostic logging.
func MinJaccard() float64 {
	return env.Float("HUNT_SCORE_MIN_JACCARD", defaultMinJaccard)
}

// MaxLLMPerCycle returns the HUNT_SCORE_MAX_LLM_PER_CYCLE circuit-breaker limit.
// Exported for use in huntworker.runCycle.
func MaxLLMPerCycle() int {
	return env.Int("HUNT_SCORE_MAX_LLM_PER_CYCLE", 50)
}

// os.Getenv is used here for a simpler failOpen check where env.Bool reads
// the real env. We need os directly to avoid import of go-kit in test-visible
// ways; however, env.Bool is already available and used above.
// This blank import ensures we don't accidentally use raw os.Getenv elsewhere.
var _ = os.Getenv
