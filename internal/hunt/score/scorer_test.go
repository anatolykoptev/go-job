package score

// Tests for the hybrid Jaccard→LLM cascade scorer (Phase 4).
//
// RED-on-revert guarantee: each test is designed so that removing or
// reverting the targeted production code causes that test to FAIL.
// Evidence for each test is documented inline.

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// freshProfile returns a minimal non-nil ScoringProfile for tests.
func freshProfile() *ScoringProfile {
	return &ScoringProfile{
		Seniority:      "Staff",
		CoreSkills:     []string{"Go", "Rust", "PostgreSQL", "distributed systems"},
		AdjacentSkills: []string{"Kubernetes", "Kafka"},
		TargetDomains:  []string{"AI infrastructure", "developer tools"},
		CompFloorUSD:   250000,
		Locations:      []string{"Remote (US)", "San Francisco Bay Area"},
		WorkAuth:       "US authorized, no sponsorship",
		AvoidSignals:   []string{"sales", "marketing", "event"},
	}
}

// stubLLM returns a fixed success response and counts invocations.
type stubLLM struct {
	calls atomic.Int64
	reply string
}

func (s *stubLLM) call(ctx context.Context, prompt string) (string, error) {
	s.calls.Add(1)
	if s.reply == "" {
		return `{
  "fit_score": 80,
  "fit_reasons": ["Go matches core stack"],
  "fit_gaps": [],
  "success_band": "MODERATE",
  "success_reasoning": "well-matched on seniority and stack",
  "over_under": "well_matched"
}`, nil
	}
	return s.reply, nil
}

// ---------------------------------------------------------------------------
// Test 1: Test_Scorer_SkipsPreFiltered
// ---------------------------------------------------------------------------
//
// Cascade cost-guard: stale or sub-Jaccard jobs must never reach the LLM.
// RED-on-revert: remove the recency pre-gate → stale job calls LLM.
//                remove the Jaccard pre-filter → sub-Jaccard job calls LLM.

func Test_Scorer_SkipsPreFiltered(t *testing.T) {
	prof := freshProfile()

	// --- stale job: posted > 48h ago ---
	t.Run("stale_job_no_llm_call", func(t *testing.T) {
		llm := &stubLLM{}
		deps := ScorerDeps{
			Jaccard: func(kw, text string) float64 { return 50 }, // well above threshold
			LLM:     llm.call,
		}
		postedAt := time.Now().Add(-72 * time.Hour) // 3 days old
		job := hunt.Job{
			Title:       "Senior Go Engineer",
			Description: "We need Go Rust PostgreSQL distributed systems Kubernetes expert",
			PostedAt:    &postedAt,
		}

		t.Setenv("HUNT_SCORE_FAIL_OPEN", "true")
		result := Score(context.Background(), prof, job, deps)

		assert.Equal(t, int64(0), llm.calls.Load(), "stale job must NOT call LLM")
		assert.Equal(t, "stale", result.FitBand, "stale job FitBand must be 'stale'")
	})

	// --- job with nil PostedAt: also stale (no date = skip LLM) ---
	t.Run("nil_posted_at_no_llm_call", func(t *testing.T) {
		llm := &stubLLM{}
		deps := ScorerDeps{
			Jaccard: func(kw, text string) float64 { return 50 },
			LLM:     llm.call,
		}
		job := hunt.Job{
			Title:       "Senior Go Engineer",
			Description: "Go Rust PostgreSQL distributed systems Kubernetes",
			PostedAt:    nil, // no date
		}

		t.Setenv("HUNT_SCORE_FAIL_OPEN", "true")
		result := Score(context.Background(), prof, job, deps)

		assert.Equal(t, int64(0), llm.calls.Load(), "nil-PostedAt job must NOT call LLM")
		assert.Equal(t, "stale", result.FitBand, "nil-PostedAt FitBand must be 'stale'")
	})

	// --- sub-Jaccard job: keyword overlap below threshold ---
	t.Run("sub_jaccard_job_no_llm_call", func(t *testing.T) {
		llm := &stubLLM{}
		deps := ScorerDeps{
			// Jaccard returns 5 — below default threshold of 8.
			Jaccard: func(kw, text string) float64 { return 5 },
			LLM:     llm.call,
		}
		postedAt := time.Now().Add(-1 * time.Hour)
		job := hunt.Job{
			Title:       "Marketing Manager",
			Description: "Sales event management brand awareness",
			PostedAt:    &postedAt,
		}

		t.Setenv("HUNT_SCORE_FAIL_OPEN", "true")
		result := Score(context.Background(), prof, job, deps)

		assert.Equal(t, int64(0), llm.calls.Load(), "sub-Jaccard job must NOT call LLM")
		assert.Equal(t, "reject", result.FitBand, "sub-Jaccard FitBand must be 'reject'")
	})

	// --- survivor job: fresh + above-Jaccard threshold → LLM called exactly once ---
	t.Run("survivor_calls_llm_once", func(t *testing.T) {
		llm := &stubLLM{}
		deps := ScorerDeps{
			// Jaccard returns 50 — well above threshold.
			Jaccard: func(kw, text string) float64 { return 50 },
			LLM:     llm.call,
		}
		postedAt := time.Now().Add(-1 * time.Hour)
		job := hunt.Job{
			Title:       "Senior Go Engineer",
			Description: "Go Rust PostgreSQL distributed systems AI infrastructure",
			PostedAt:    &postedAt,
		}

		t.Setenv("HUNT_SCORE_FAIL_OPEN", "true")
		result := Score(context.Background(), prof, job, deps)

		assert.Equal(t, int64(1), llm.calls.Load(), "survivor must call LLM exactly once")
		// LLM was called, so result should NOT be stale or reject.
		assert.NotEqual(t, "stale", result.FitBand)
		assert.NotEqual(t, "reject", result.FitBand)
	})
}

// ---------------------------------------------------------------------------
// Test 2: Test_Prompt_And_Parse_NoFakePrecision
// ---------------------------------------------------------------------------
//
// Honesty enforcement: success_band must be an enum, never a fake percentage.
// RED-on-revert: remove enum-clamp → garbage band reaches caller.

func Test_Prompt_And_Parse_NoFakePrecision(t *testing.T) {
	// --- valid response parses correctly ---
	t.Run("valid_json_parses", func(t *testing.T) {
		raw := `{
  "fit_score": 75,
  "fit_reasons": ["Go matches", "distributed systems"],
  "fit_gaps": ["missing k8s cert"],
  "success_band": "MODERATE",
  "success_reasoning": "well-matched on seniority and stack",
  "over_under": "well_matched"
}`
		result, err := parseScoreResponse(raw, 30)
		require.NoError(t, err)
		assert.Equal(t, 75, result.FitScore)
		assert.Equal(t, "MODERATE", result.SuccessBand)
		assert.Equal(t, "well_matched", result.OverUnder)
		assert.Equal(t, []string{"Go matches", "distributed systems"}, result.FitReasons)
		assert.Equal(t, []string{"missing k8s cert"}, result.FitGaps)
	})

	// --- garbage success_band is clamped to "MODERATE" ---
	// RED-on-revert: remove clamp → "EXCELLENT" passes through raw.
	t.Run("garbage_success_band_clamped", func(t *testing.T) {
		raw := `{
  "fit_score": 80,
  "fit_reasons": [],
  "fit_gaps": [],
  "success_band": "EXCELLENT",
  "success_reasoning": "amazing candidate",
  "over_under": "well_matched"
}`
		result, err := parseScoreResponse(raw, 30)
		require.NoError(t, err)
		assert.Equal(t, "MODERATE", result.SuccessBand,
			"unknown success_band must be clamped to MODERATE")
	})

	// --- garbage over_under is clamped to "well_matched" ---
	t.Run("garbage_over_under_clamped", func(t *testing.T) {
		raw := `{
  "fit_score": 70,
  "fit_reasons": [],
  "fit_gaps": [],
  "success_band": "STRONG",
  "success_reasoning": "good match",
  "over_under": "perfectly_matched"
}`
		result, err := parseScoreResponse(raw, 30)
		require.NoError(t, err)
		assert.Equal(t, "well_matched", result.OverUnder,
			"unknown over_under must be clamped to well_matched")
	})

	// --- fit_score below 0 is clamped to 0 ---
	t.Run("fit_score_below_zero_clamped", func(t *testing.T) {
		raw := `{
  "fit_score": -5,
  "fit_reasons": [],
  "fit_gaps": [],
  "success_band": "LONGSHOT",
  "success_reasoning": "not a match",
  "over_under": "under_qualified"
}`
		result, err := parseScoreResponse(raw, 30)
		require.NoError(t, err)
		assert.Equal(t, 0, result.FitScore, "fit_score < 0 must clamp to 0")
	})

	// --- fit_score above 100 is clamped to 100 ---
	t.Run("fit_score_above_100_clamped", func(t *testing.T) {
		raw := `{
  "fit_score": 110,
  "fit_reasons": [],
  "fit_gaps": [],
  "success_band": "STRONG",
  "success_reasoning": "great",
  "over_under": "well_matched"
}`
		result, err := parseScoreResponse(raw, 30)
		require.NoError(t, err)
		assert.Equal(t, 100, result.FitScore, "fit_score > 100 must clamp to 100")
	})

	// --- fake-precision stripper at STORE boundary (MAJOR 1 guard) ---
	// RED-on-revert: revert stripPercentages → persisted SuccessReasoning keeps "73%".
	// This test feeds parseScoreResponse an LLM-shaped JSON with percentages in
	// success_reasoning AND fit_gaps, then asserts the returned ScoreResult has
	// NO "%" in either field. A clean reasoning must pass through unchanged.
	t.Run("fake_precision_stripped_from_stored_result", func(t *testing.T) {
		// LLM ignored the prompt instruction and included percentages in ALL
		// three free-text surfaces — success_reasoning, fit_gaps AND fit_reasons.
		// fit_reasons is rendered on the card's "Why you:" line (Phase 5), so it
		// must be stripped at the same store boundary as the other two.
		dirty := `{
  "fit_score": 72,
  "fit_reasons": ["Go expert, matches ~90% of the JD", "distributed systems"],
  "fit_gaps": ["missing k8s cert (~40% of JD focus)", "salary floor misaligned"],
  "success_band": "MODERATE",
  "success_reasoning": "strong match, ~73% likely given the pool",
  "over_under": "well_matched"
}`
		result, err := parseScoreResponse(dirty, 30)
		require.NoError(t, err)

		// success_reasoning must have no "%" character.
		assert.NotContains(t, result.SuccessReasoning, "%",
			"persisted SuccessReasoning must be percentage-free; got: %q", result.SuccessReasoning)

		// Each fit_gaps entry must have no "%" character.
		for _, gap := range result.FitGaps {
			assert.NotContains(t, gap, "%",
				"persisted FitGaps entry must be percentage-free; got: %q", gap)
		}

		// Each fit_reasons entry must have no "%" character.
		// RED-on-revert: revert the FitReasons sanitization (scorer.go) → the
		// "~90%" survives into the "Why you:" card line and this assertion fails.
		for _, reason := range result.FitReasons {
			assert.NotContains(t, reason, "%",
				"persisted FitReasons entry must be percentage-free; got: %q", reason)
		}
		// The non-percentage portions are preserved (stripper is surgical).
		assert.Equal(t, []string{"Go expert, matches of the JD", "distributed systems"}, result.FitReasons)
	})

	// --- clean reasoning passes through unchanged ---
	// RED-on-revert: if stripPercentages removes too much, clean text is mutated.
	t.Run("clean_reasoning_passes_through_unchanged", func(t *testing.T) {
		clean := `{
  "fit_score": 80,
  "fit_reasons": ["Go expert"],
  "fit_gaps": ["missing k8s cert"],
  "success_band": "STRONG",
  "success_reasoning": "strong seniority and domain match; missing only a nice-to-have cert",
  "over_under": "well_matched"
}`
		result, err := parseScoreResponse(clean, 30)
		require.NoError(t, err)
		assert.Equal(t, "strong seniority and domain match; missing only a nice-to-have cert",
			result.SuccessReasoning,
			"clean SuccessReasoning must not be mutated by the stripper")
		assert.Equal(t, []string{"missing k8s cert"}, result.FitGaps,
			"clean FitGaps must not be mutated by the stripper")
	})
}

// ---------------------------------------------------------------------------
// Test 3: Parse tests (valid / fenced / junk JSON)
// ---------------------------------------------------------------------------
//
// Verifies the three JSON input scenarios the scorer must handle.

func Test_ParseScoreResponse(t *testing.T) {
	// --- valid JSON → correct ScoreResult ---
	t.Run("valid_json", func(t *testing.T) {
		raw := `{"fit_score":85,"fit_reasons":["Go expert"],"fit_gaps":["k8s"],"success_band":"STRONG","success_reasoning":"strong seniority match","over_under":"well_matched"}`
		result, err := parseScoreResponse(raw, 50)
		require.NoError(t, err)
		assert.Equal(t, 85, result.FitScore)
		assert.Equal(t, "STRONG", result.SuccessBand)
		assert.Equal(t, "well_matched", result.OverUnder)
	})

	// --- fenced JSON (```json ... ```) → stripped + parsed ---
	// RED-on-revert: remove StripMarkdownFences call → fenced JSON fails to parse.
	t.Run("fenced_json_stripped_and_parsed", func(t *testing.T) {
		raw := "```json\n" + `{"fit_score":72,"fit_reasons":[],"fit_gaps":["c++"],"success_band":"MODERATE","success_reasoning":"good but niche","over_under":"well_matched"}` + "\n```"
		result, err := parseScoreResponse(raw, 40)
		require.NoError(t, err)
		assert.Equal(t, 72, result.FitScore)
		assert.Equal(t, "MODERATE", result.SuccessBand)
	})

	// --- junk/invalid JSON → fail-open returns FitBand:"unscored" ---
	// RED-on-revert: remove fail-open → error returned instead of unscored result.
	t.Run("junk_json_fail_open", func(t *testing.T) {
		t.Setenv("HUNT_SCORE_FAIL_OPEN", "true")
		raw := "I couldn't generate valid JSON sorry"
		result, err := parseScoreResponse(raw, 10)
		// Fail-open: no error, but FitBand is "unscored" and FitScore is the jaccard fallback.
		require.NoError(t, err)
		assert.Equal(t, "unscored", result.FitBand)
		assert.Equal(t, 10, result.FitScore) // jaccard fallback score passed in
	})

	// --- fail-closed (HUNT_SCORE_FAIL_OPEN=false): returns error on bad JSON ---
	t.Run("junk_json_fail_closed_returns_error", func(t *testing.T) {
		t.Setenv("HUNT_SCORE_FAIL_OPEN", "false")
		raw := "not json at all"
		_, err := parseScoreResponse(raw, 10)
		assert.Error(t, err, "fail-closed must return error on unparseable response")
	})
}

// ---------------------------------------------------------------------------
// Test 4: Prompt construction sanity
// ---------------------------------------------------------------------------
//
// Verifies that the LLM prompt includes honesty instructions and key profile fields.

func Test_BuildScorerPrompt_ContainsHonestyGuard(t *testing.T) {
	prof := freshProfile()
	postedAt := time.Now().Add(-2 * time.Hour)
	job := hunt.Job{
		Title:       "Staff Software Engineer",
		Company:     "Acme Corp",
		Description: "We need Go, Rust, distributed systems expertise",
		PostedAt:    &postedAt,
	}

	prompt := buildScorerPrompt(prof, job)

	// Honesty guards must be present in the prompt.
	assert.True(t, strings.Contains(prompt, "NEVER") || strings.Contains(prompt, "never"),
		"prompt must contain 'NEVER' or 'never' — honesty instruction")
	assert.True(t, strings.Contains(prompt, "success_band"),
		"prompt must reference the success_band field")
	assert.True(t, strings.Contains(prompt, "STRONG") || strings.Contains(prompt, "MODERATE") || strings.Contains(prompt, "LONGSHOT"),
		"prompt must enumerate the allowed success_band values")
	assert.True(t, strings.Contains(prompt, "fit_score"),
		"prompt must reference the fit_score field")

	// Profile fields must appear in the prompt.
	assert.Contains(t, prompt, "Staff", "prompt must include seniority")
	assert.Contains(t, prompt, "Go", "prompt must include core skills")

	// Job fields must appear in the prompt.
	assert.Contains(t, prompt, "Staff Software Engineer", "prompt must include job title")
}

// ---------------------------------------------------------------------------
// Test 5: Full cascade with env var tuning
// ---------------------------------------------------------------------------
//
// HUNT_SCORE_MIN_JACCARD env var controls the Jaccard threshold.

func Test_Scorer_JaccardThresholdFromEnv(t *testing.T) {
	prof := freshProfile()
	postedAt := time.Now().Add(-1 * time.Hour)

	// With HUNT_SCORE_MIN_JACCARD=20, a score of 15 should be rejected.
	t.Run("custom_threshold_rejects", func(t *testing.T) {
		t.Setenv("HUNT_SCORE_MIN_JACCARD", "20")
		llm := &stubLLM{}
		deps := ScorerDeps{
			Jaccard: func(kw, text string) float64 { return 15 },
			LLM:     llm.call,
		}
		job := hunt.Job{Title: "Go Engineer", Description: "Go systems", PostedAt: &postedAt}

		result := Score(context.Background(), prof, job, deps)
		assert.Equal(t, int64(0), llm.calls.Load(), "score 15 below threshold 20 must skip LLM")
		assert.Equal(t, "reject", result.FitBand)
	})

	// With HUNT_SCORE_MIN_JACCARD=20, a score of 25 should proceed.
	t.Run("custom_threshold_passes", func(t *testing.T) {
		t.Setenv("HUNT_SCORE_MIN_JACCARD", "20")
		llm := &stubLLM{}
		deps := ScorerDeps{
			Jaccard: func(kw, text string) float64 { return 25 },
			LLM:     llm.call,
		}
		job := hunt.Job{Title: "Go Engineer", Description: "Go systems", PostedAt: &postedAt}

		result := Score(context.Background(), prof, job, deps)
		assert.Equal(t, int64(1), llm.calls.Load(), "score 25 above threshold 20 must call LLM")
		assert.NotEqual(t, "reject", result.FitBand)
	})
}

// ---------------------------------------------------------------------------
// Test 6: nil profile → scoring disabled, returns unscored
// ---------------------------------------------------------------------------

func Test_Scorer_NilProfile_ReturnsUnscored(t *testing.T) {
	llm := &stubLLM{}
	deps := ScorerDeps{
		Jaccard: func(kw, text string) float64 { return 50 },
		LLM:     llm.call,
	}
	postedAt := time.Now().Add(-1 * time.Hour)
	job := hunt.Job{Title: "Go Engineer", Description: "Go systems", PostedAt: &postedAt}

	result := Score(context.Background(), nil, job, deps)

	assert.Equal(t, int64(0), llm.calls.Load(), "nil profile must not call LLM")
	assert.Equal(t, "unscored", result.FitBand, "nil profile must return unscored")
}

// ---------------------------------------------------------------------------
// Test 7: LLMResult transient signal (Phase 6)
// ---------------------------------------------------------------------------
//
// Verifies that ScoreResult.LLMResult is set correctly on each LLM path.
// Only LLM-path results set a non-empty LLMResult; pre-LLM short-circuits
// (stale/reject) leave LLMResult empty so the worker can distinguish them.
//
// RED-on-revert:
//   - Remove result.LLMResult = "ok" assignment → test fails (field stays "").
//   - Remove result.LLMResult = "enum_clamp" assignment → test fails.
//   - Remove result.LLMResult = "parse_fail" assignment → test fails.

func Test_LLMResult_Signal(t *testing.T) {
	prof := freshProfile()
	postedAt := time.Now().Add(-1 * time.Hour)
	freshJob := hunt.Job{
		Title:       "Senior Go Engineer",
		Description: "Go Rust PostgreSQL distributed systems AI infrastructure Kubernetes",
		PostedAt:    &postedAt,
	}

	// Case 1: clean LLM response → LLMResult == "ok"
	t.Run("clean_json_ok", func(t *testing.T) {
		t.Setenv("HUNT_SCORE_FAIL_OPEN", "true")
		llm := &stubLLM{} // returns valid JSON with well-known fields
		deps := ScorerDeps{
			Jaccard: func(kw, text string) float64 { return 50 },
			LLM:     llm.call,
		}
		result := Score(context.Background(), prof, freshJob, deps)
		assert.Equal(t, "ok", result.LLMResult,
			"clean LLM response must set LLMResult='ok'; RED-on-revert: remove the assignment → field empty")
		assert.True(t, result.LLMCalled, "LLMCalled must be true when LLM was invoked")
	})

	// Case 2: unknown success_band → enum-clamped → LLMResult == "enum_clamp"
	// The LLM returned an unrecognised success_band value; parseScoreResponse
	// clamps it to "MODERATE" and sets LLMResult="enum_clamp".
	t.Run("unknown_success_band_enum_clamp", func(t *testing.T) {
		t.Setenv("HUNT_SCORE_FAIL_OPEN", "true")
		llm := &stubLLM{reply: `{
  "fit_score": 70,
  "fit_reasons": ["Go"],
  "fit_gaps": [],
  "success_band": "EXCELLENT",
  "success_reasoning": "top match",
  "over_under": "well_matched"
}`}
		deps := ScorerDeps{
			Jaccard: func(kw, text string) float64 { return 50 },
			LLM:     llm.call,
		}
		result := Score(context.Background(), prof, freshJob, deps)
		assert.Equal(t, "enum_clamp", result.LLMResult,
			"unknown success_band must set LLMResult='enum_clamp'; RED-on-revert: remove the assignment → field stays 'ok'")
	})

	// Case 3: non-JSON response → parse_fail (fail-open) → LLMResult == "parse_fail"
	t.Run("non_json_parse_fail", func(t *testing.T) {
		t.Setenv("HUNT_SCORE_FAIL_OPEN", "true")
		llm := &stubLLM{reply: "Sorry, I cannot score this job."}
		deps := ScorerDeps{
			Jaccard: func(kw, text string) float64 { return 50 },
			LLM:     llm.call,
		}
		result := Score(context.Background(), prof, freshJob, deps)
		assert.Equal(t, "parse_fail", result.LLMResult,
			"non-JSON LLM response must set LLMResult='parse_fail'; RED-on-revert: remove the assignment → field empty")
		assert.Equal(t, hunt.FitBandUnscored, result.FitBand, "parse_fail must result in unscored (fail-open)")
	})

	// Case 4: pre-LLM short-circuits leave LLMResult empty
	t.Run("stale_job_no_llm_result", func(t *testing.T) {
		stalePostedAt := time.Now().Add(-72 * time.Hour)
		staleJob := hunt.Job{
			Title:    "Go Engineer",
			PostedAt: &stalePostedAt,
		}
		llm := &stubLLM{}
		deps := ScorerDeps{
			Jaccard: func(kw, text string) float64 { return 50 },
			LLM:     llm.call,
		}
		result := Score(context.Background(), prof, staleJob, deps)
		assert.Equal(t, "", result.LLMResult,
			"stale short-circuit must leave LLMResult empty (pre-LLM, not an LLM outcome)")
		assert.Equal(t, "stale", result.FitBand)
	})

	t.Run("reject_job_no_llm_result", func(t *testing.T) {
		llm := &stubLLM{}
		deps := ScorerDeps{
			Jaccard: func(kw, text string) float64 { return 2 }, // below threshold
			LLM:     llm.call,
		}
		result := Score(context.Background(), prof, freshJob, deps)
		assert.Equal(t, "", result.LLMResult,
			"jaccard-reject short-circuit must leave LLMResult empty (pre-LLM, not an LLM outcome)")
		assert.Equal(t, "reject", result.FitBand)
	})
}

// ---------------------------------------------------------------------------
// Test 8: ScoreForce bypasses recency and Jaccard pre-gates
// ---------------------------------------------------------------------------
//
// ScoreForce must call the LLM regardless of job staleness or sub-Jaccard overlap,
// and must return a real fit band (not "stale" or "reject").
//
// RED-on-revert evidence:
//   - (a) stale sub-case: replace ScoreForce body with a Score call →
//     stale job returns FitBandStale without calling the LLM; assertion fails.
//   - (b) sub-jaccard sub-case: same revert → returns FitBandReject; assertion fails.
//   - (c) nil-profile sub-case: remove Stage 0 guard → panics or calls LLM on nil.

func TestScoreForce_BypassesRecencyAndJaccard(t *testing.T) {
	prof := freshProfile()

	// --- (a) stale job (PostedAt > 48h) still calls LLM and returns real band ---
	t.Run("stale_job_calls_llm", func(t *testing.T) {
		llm := &stubLLM{} // returns valid JSON with fit_score=80, band="strong"
		deps := ScorerDeps{
			Jaccard: func(kw, text string) float64 { return 50 }, // not called by ScoreForce
			LLM:     llm.call,
		}
		postedAt := time.Now().Add(-72 * time.Hour) // 3 days old — Score() would return "stale"
		job := hunt.Job{
			Title:       "Senior Go Engineer",
			Description: "We need Go Rust PostgreSQL distributed systems expert",
			PostedAt:    &postedAt,
		}

		t.Setenv("HUNT_SCORE_FAIL_OPEN", "true")
		result := ScoreForce(context.Background(), prof, job, deps)

		assert.Equal(t, int64(1), llm.calls.Load(), "ScoreForce must call LLM on stale job")
		assert.NotEqual(t, hunt.FitBandStale, result.FitBand,
			"ScoreForce must not return stale band — recency gate is bypassed")
		assert.True(t, result.LLMCalled, "ScoreForce result must have LLMCalled=true")
	})

	// --- (b) sub-Jaccard job still calls LLM and returns real band ---
	t.Run("sub_jaccard_calls_llm", func(t *testing.T) {
		llm := &stubLLM{} // returns valid JSON with fit_score=80
		deps := ScorerDeps{
			Jaccard: func(kw, text string) float64 { return 2 }, // well below min threshold of 8
			LLM:     llm.call,
		}
		postedAt := time.Now().Add(-1 * time.Hour)
		job := hunt.Job{
			Title:       "Marketing Manager",
			Description: "Sales event management brand awareness campaigns",
			PostedAt:    &postedAt,
		}

		t.Setenv("HUNT_SCORE_FAIL_OPEN", "true")
		result := ScoreForce(context.Background(), prof, job, deps)

		assert.Equal(t, int64(1), llm.calls.Load(), "ScoreForce must call LLM on sub-Jaccard job")
		assert.NotEqual(t, hunt.FitBandReject, result.FitBand,
			"ScoreForce must not return reject band — Jaccard gate is bypassed")
		assert.True(t, result.LLMCalled, "ScoreForce result must have LLMCalled=true")
	})

	// --- (c) nil profile returns unscored without calling LLM ---
	t.Run("nil_profile_unscored_no_llm", func(t *testing.T) {
		llm := &stubLLM{}
		deps := ScorerDeps{
			Jaccard: func(kw, text string) float64 { return 50 },
			LLM:     llm.call,
		}
		postedAt := time.Now().Add(-1 * time.Hour)
		job := hunt.Job{
			Title:       "Staff Engineer",
			Description: "Go Rust distributed systems",
			PostedAt:    &postedAt,
		}

		result := ScoreForce(context.Background(), nil, job, deps)

		assert.Equal(t, int64(0), llm.calls.Load(), "nil profile must not call LLM")
		assert.Equal(t, hunt.FitBandUnscored, result.FitBand,
			"nil profile must return unscored — Stage 0 guard still applies")
	})
}


// ---------------------------------------------------------------------------
// Test 9: stripMarkdownFences — table-driven unit tests
// ---------------------------------------------------------------------------
//
// Pure deterministic helper; tests CALL the real function (not a copy).
// RED-on-revert: if stripMarkdownFences is deleted or returns its input
// unchanged, the fenced cases fail. If TrimSpace is removed, the whitespace
// cases fail.

func Test_StripMarkdownFences(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "no_fences_returned_unchanged",
			raw:  `{"fit_score":80}`,
			want: `{"fit_score":80}`,
		},
		{
			name: "json_tagged_fence_stripped",
			raw:  "```json\n{\"fit_score\":80}\n```",
			want: `{"fit_score":80}`,
		},
		{
			name: "plain_fence_stripped",
			raw:  "```\n{\"fit_score\":80}\n```",
			want: `{"fit_score":80}`,
		},
		{
			name: "surrounding_whitespace_trimmed",
			raw:  "  \n  {\"fit_score\":80}  \n  ",
			want: `{"fit_score":80}`,
		},
		{
			name: "json_fence_with_leading_trailing_spaces",
			raw:  "  ```json\n  {\"fit_score\":42}  \n```  ",
			want: `{"fit_score":42}`,
		},
		{
			name: "plain_fence_with_whitespace_inside",
			raw:  "```\n  {\"key\":\"val\"}  \n```",
			want: `{"key":"val"}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := stripMarkdownFences(tc.raw)
			// RED-on-revert: if stripMarkdownFences is reverted to return raw unchanged,
			// all fenced cases fail immediately (fenced raw != stripped content).
			assert.Equal(t, tc.want, got,
				"stripMarkdownFences(%q) = %q; want %q", tc.raw, got, tc.want)
		})
	}
}

// ---------------------------------------------------------------------------
// Test 10: fitBandFromScore — table-driven unit tests
// ---------------------------------------------------------------------------
//
// Pure deterministic function; tests CALL the real fitBandFromScore (not a copy).
// Covers every band plus the boundary scores for each.
// RED-on-revert: if fitBandFromScore is deleted or thresholds changed, the
// boundary assertions fail.

func Test_FitBandFromScore(t *testing.T) {
	tests := []struct {
		score int
		want  string
	}{
		// "strong" band: score >= 75
		{score: 75, want: "strong"},  // lower boundary
		{score: 100, want: "strong"}, // upper boundary
		{score: 90, want: "strong"},  // mid-band

		// "moderate" band: 50 <= score < 75
		{score: 74, want: "moderate"}, // just below strong boundary
		{score: 50, want: "moderate"}, // lower boundary
		{score: 62, want: "moderate"}, // mid-band

		// "weak" band: 25 <= score < 50
		{score: 49, want: "weak"}, // just below moderate boundary
		{score: 25, want: "weak"}, // lower boundary
		{score: 37, want: "weak"}, // mid-band

		// "low" band: score < 25
		{score: 24, want: "low"}, // just below weak boundary
		{score: 0, want: "low"},  // zero
		{score: -1, want: "low"}, // below zero (default branch)
	}

	for _, tc := range tests {
		t.Run(fmt.Sprintf("score_%d_band_%s", tc.score, tc.want), func(t *testing.T) {
			got := fitBandFromScore(tc.score)
			// RED-on-revert: if fitBandFromScore is reverted to return "" for all inputs,
			// every assertion here fails (no band string equals "").
			assert.Equal(t, tc.want, got,
				"fitBandFromScore(%d) = %q; want %q", tc.score, got, tc.want)
		})
	}
}
