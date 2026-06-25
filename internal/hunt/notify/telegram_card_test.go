package notify_test

// telegram_card_test.go: fitness functions #2 — fit-card format.
//
// Full card format spec (Phase 5):
//   [<fit_band> · <fit_score>] <title>
//   <company> · <location/remote> · <salary band if present>
//
//   Why you: <≤2 fit_reasons joined by "; ">
//   Gaps: <≤2 fit_gaps joined by "; ">
//   Success: <SUCCESS_BAND> — <success_reasoning>
//
//   <apply url>
//
// Degraded card (score==nil OR FitBand=="unscored"):
//   [fresh · recency-only] <title>
//   <company> · <location/remote> · <salary band if present>
//
//   <apply url>
//
// CRITICAL invariant: the "Success:" line MUST NEVER contain a digit.
// This is fitness function #2 from the architecture spec — "honesty design".
//
// RED-on-revert:
//   Remove the fit-card rewrite in formatJobMsg → header stays "[greenhouse] Senior Go Engineer",
//   all tests asserting the new format fail.
//   Remove the success-line no-digit guard → digit assertion passes but the production
//   code is wrong — the test catches it.

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/anatolykoptev/go_job/internal/hunt/notify"
)

// scoreFor builds a ScoreResult for card tests.
func scoreFor(fitScore int, fitBand, successBand, successReasoning string, fitReasons, fitGaps []string) *hunt.ScoreResult {
	return &hunt.ScoreResult{
		FitScore:         fitScore,
		FitBand:          fitBand,
		SuccessBand:      successBand,
		SuccessReasoning: successReasoning,
		FitReasons:       fitReasons,
		FitGaps:          fitGaps,
	}
}

// jobWith builds a Job for card tests.
func jobWith(title, company, location, remote string, salaryMin, salaryMax int) hunt.Job {
	postedAt := time.Now().Add(-1 * time.Hour)
	return hunt.Job{
		Title:    title,
		Company:  company,
		Location: location,
		Remote:   remote,
		SalaryMin: salaryMin,
		SalaryMax: salaryMax,
		URL:      "https://jobs.acme.io/apply",
		Source:   "greenhouse",
		PostedAt: &postedAt,
	}
}

// Test_FormatJobMsg_FullCard verifies the full fit-card format.
func Test_FormatJobMsg_FullCard(t *testing.T) {
	sr := scoreFor(
		82, "high",
		"MODERATE",
		"well-matched on seniority + stack; comp band reaches your floor",
		[]string{"Go + distributed systems match core stack", "AI-infra domain on-target"},
		[]string{"no explicit k8s", "they list Temporal (adjacent to your go-workflow)"},
	)
	j := jobWith("Senior Backend Engineer (Go)", "Anthropic", "Remote", "yes", 220000, 300000)

	card := notify.FormatJobMsgForTest(j, sr)

	// Header must include fit_band and fit_score.
	if !strings.Contains(card, "82") {
		t.Errorf("full card must contain fit_score=82 in header, got:\n%s", card)
	}
	if !strings.Contains(card, "high") {
		t.Errorf("full card must contain fit_band=high in header, got:\n%s", card)
	}

	// "Why you:" line must be present.
	if !strings.Contains(card, "Why you:") {
		t.Errorf("full card must contain 'Why you:' line, got:\n%s", card)
	}
	if !strings.Contains(card, "Go + distributed systems match core stack") {
		t.Errorf("full card must contain first fit_reason, got:\n%s", card)
	}

	// "Gaps:" line must be present.
	if !strings.Contains(card, "Gaps:") {
		t.Errorf("full card must contain 'Gaps:' line, got:\n%s", card)
	}
	if !strings.Contains(card, "no explicit k8s") {
		t.Errorf("full card must contain first fit_gap, got:\n%s", card)
	}

	// "Success:" line must be present and contain the band enum.
	if !strings.Contains(card, "Success:") {
		t.Errorf("full card must contain 'Success:' line, got:\n%s", card)
	}
	if !strings.Contains(card, "MODERATE") {
		t.Errorf("full card must contain success_band=MODERATE, got:\n%s", card)
	}

	// CRITICAL: success line must match "^Success: <BAND> —" (band enum, then dash, then prose).
	successLineRE := regexp.MustCompile(`(?m)^Success: (STRONG|MODERATE|LONGSHOT) —`)
	if !successLineRE.MatchString(card) {
		t.Errorf("success line must match 'Success: <BAND> —', got:\n%s", card)
	}

	// CRITICAL: no digit anywhere in the success line (fitness function #2 — no fake precision).
	lines := strings.Split(card, "\n")
	digitRE := regexp.MustCompile(`\d`)
	for _, line := range lines {
		if strings.HasPrefix(line, "Success:") && digitRE.MatchString(line) {
			t.Errorf("SUCCESS LINE MUST NEVER CONTAIN A DIGIT — honesty design violation.\nLine: %q\nFull card:\n%s", line, card)
		}
	}

	// Apply URL must be present.
	if !strings.Contains(card, "https://jobs.acme.io/apply") {
		t.Errorf("full card must contain apply URL, got:\n%s", card)
	}
}

// Test_FormatJobMsg_FullCard_AllSuccessBands ensures no digit for all three success bands.
func Test_FormatJobMsg_FullCard_AllSuccessBands(t *testing.T) {
	bands := []string{"STRONG", "MODERATE", "LONGSHOT"}
	digitRE := regexp.MustCompile(`\d`)

	for _, band := range bands {
		sr := scoreFor(75, "high", band,
			"seniority and stack align well for this role",
			[]string{"Go expertise"}, []string{"minor gap"},
		)
		j := jobWith("Staff Engineer", "ExampleCo", "San Francisco", "yes", 0, 0)
		card := notify.FormatJobMsgForTest(j, sr)

		lines := strings.Split(card, "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "Success:") && digitRE.MatchString(line) {
				t.Errorf("band=%s: success line must not contain digit.\nLine: %q\nFull card:\n%s", band, line, card)
			}
		}
	}
}

// Test_FormatJobMsg_DegradedCard_NilScore verifies the degraded card when score is nil.
func Test_FormatJobMsg_DegradedCard_NilScore(t *testing.T) {
	j := jobWith("Senior Backend Engineer (Go)", "Anthropic", "Remote", "yes", 220000, 300000)

	card := notify.FormatJobMsgForTest(j, nil)

	// Degraded header.
	if !strings.Contains(card, "fresh · recency-only") {
		t.Errorf("degraded card (nil score) must contain '[fresh · recency-only]' header, got:\n%s", card)
	}

	// Must NOT contain fit sections.
	if strings.Contains(card, "Why you:") {
		t.Errorf("degraded card must NOT contain 'Why you:' line, got:\n%s", card)
	}
	if strings.Contains(card, "Gaps:") {
		t.Errorf("degraded card must NOT contain 'Gaps:' line, got:\n%s", card)
	}
	if strings.Contains(card, "Success:") {
		t.Errorf("degraded card must NOT contain 'Success:' line, got:\n%s", card)
	}

	// Apply URL must still be present.
	if !strings.Contains(card, "https://jobs.acme.io/apply") {
		t.Errorf("degraded card must contain apply URL, got:\n%s", card)
	}
}

// Test_FormatJobMsg_DegradedCard_Unscored verifies degraded card for FitBand=="unscored".
func Test_FormatJobMsg_DegradedCard_Unscored(t *testing.T) {
	sr := &hunt.ScoreResult{FitBand: "unscored"}
	j := jobWith("Backend Engineer", "StartupCo", "", "yes", 0, 0)

	card := notify.FormatJobMsgForTest(j, sr)

	// Degraded header.
	if !strings.Contains(card, "fresh · recency-only") {
		t.Errorf("degraded card (unscored) must contain '[fresh · recency-only]' header, got:\n%s", card)
	}

	// Must NOT contain fit sections.
	if strings.Contains(card, "Why you:") {
		t.Errorf("degraded card (unscored) must NOT contain 'Why you:', got:\n%s", card)
	}
	if strings.Contains(card, "Gaps:") {
		t.Errorf("degraded card (unscored) must NOT contain 'Gaps:', got:\n%s", card)
	}
	if strings.Contains(card, "Success:") {
		t.Errorf("degraded card (unscored) must NOT contain 'Success:', got:\n%s", card)
	}
}
