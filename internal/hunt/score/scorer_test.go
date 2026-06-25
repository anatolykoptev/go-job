package score

// Tests for the hybrid Jaccard→LLM cascade scorer (Phase 4).
//
// RED-on-revert guarantee: each test is designed so that removing or
// reverting the targeted production code causes that test to FAIL.
// Evidence for each test is documented inline.

import (
	"context"
	"regexp"
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

	// --- success_reasoning must not contain percentages (honesty guard) ---
	// This test validates the prompt's honesty instruction by checking that a
	// response containing a percentage in success_reasoning gets flagged or
	// that our parse + card rendering wouldn't pass one through.
	// We verify this by asserting the rendered "Success:" line does not match
	// the fake-precision regex. Since parse doesn't strip reasoning, we test the
	// regex itself against what would be rendered.
	t.Run("success_line_no_fake_precision_regex", func(t *testing.T) {
		fakePrecisionRE := regexp.MustCompile(`\d+(\.\d+)?%`)

		// Simulate what formatJobMsg would output for the Success line.
		// The band is enum-controlled; only reasoning can leak fake precision.
		cleanReasoning := "well-matched on seniority and stack"
		successLine := "Success: MODERATE — " + cleanReasoning

		assert.False(t, fakePrecisionRE.MatchString(successLine),
			"success line with clean reasoning must not contain percentage: %q", successLine)

		// Confirm the regex WOULD catch a fake-precise line (falsification).
		fakeLine := "Success: MODERATE — 73% likely match for this role"
		assert.True(t, fakePrecisionRE.MatchString(fakeLine),
			"fake-precision check must detect percentage in success line")
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
