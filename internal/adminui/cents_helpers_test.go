package adminui

import (
	"os"
	"strings"
	"testing"

	"github.com/anatolykoptev/go_job/internal/engine/jobs"
)

// TestParseDollarsToCents_IsTheOnlyInlineParseInAdminui verifies parseDollarsToCents
// is the canonical parse function in internal/adminui for cents-from-dollars conversions.
// The Makefile preflight grep is the durable enforcement; this test is the TDD Red anchor.
//
// Red-on-revert: comment out parseDollarsToCents body → compile error or wrong result.
func TestParseDollarsToCents_IsTheOnlyInlineParseInAdminui(t *testing.T) {
	cases := []struct {
		input   string
		want    int64
		wantErr bool
	}{
		{"95.00", 9500, false},
		{"150", 15000, false},
		{"150.50", 15050, false},
		{"", 0, false},
		{"-5", 0, true},
		{"not-a-number", 0, true},
	}
	for _, tc := range cases {
		got, err := parseDollarsToCents(tc.input)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseDollarsToCents(%q) = %d, nil; want error", tc.input, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("parseDollarsToCents(%q): %v", tc.input, err)
		}
		if got != tc.want {
			t.Errorf("parseDollarsToCents(%q) = %d, want %d", tc.input, got, tc.want)
		}
	}
}

// TestBuildUpworkPageData_RateUsesCentsToDollars asserts that buildUpworkPageData
// produces Rate == "$95.00/hr" when HourlyRateCents == 9500.
//
// This tests the Site 2 invariant: the fallback rate display must go through
// centsToDollars (the canonical helper) rather than an inline Sprintf("$%.2f/hr", ...).
//
// Red-on-revert: the inline fmt.Sprintf("$%.2f/hr", ...) produces the same string,
// so the test does NOT go red on a purely behavioural check. The true RED is the
// Makefile fitness-grep which catches the pattern before cleanup (see preflight target).
// This test anchors the single-code-path requirement and guards regressions.
func TestBuildUpworkPageData_RateUsesCentsToDollars(t *testing.T) {
	profile := &jobs.ResumeProfileResult{
		HourlyRateCents: 9500,
	}
	d := buildUpworkPageData(profile)
	const want = "$95.00/hr"
	if d.Rate != want {
		t.Errorf("buildUpworkPageData Rate = %q, want %q", d.Rate, want)
	}
}

// TestBuildUpworkPageData_ZeroRateIsEmpty asserts that Rate is "" when
// HourlyRateCents == 0, consistent with centsToDollars returning "" for zero.
//
// Red-on-revert: remove the `if profile.HourlyRateCents > 0` guard → Rate set to "$0.00/hr".
func TestBuildUpworkPageData_ZeroRateIsEmpty(t *testing.T) {
	profile := &jobs.ResumeProfileResult{HourlyRateCents: 0}
	d := buildUpworkPageData(profile)
	if d.Rate != "" {
		t.Errorf("buildUpworkPageData Rate for 0 cents = %q, want empty", d.Rate)
	}
}

// TestResumeEdit_NoInlineParseFloat_Fitness is a source-level fitness assertion.
// After the refactor, resume_edit.go must NOT contain an inline ParseFloat+Round
// clone for hourly_rate parsing; it must delegate to parseDollarsToCents.
//
// This is the RED anchor for Site 1. Before the fix: the pattern IS present in the
// source (confirmed by the Makefile preflight grep). After the fix: it must be absent.
//
// Red-on-revert: restore the inline parse block in resumePersonEditHandler → test fails.
func TestResumeEdit_NoInlineParseFloat_Fitness(t *testing.T) {
	src, err := os.ReadFile("resume_edit.go")
	if err != nil {
		t.Fatalf("read resume_edit.go: %v", err)
	}
	// After the refactor, the inline ParseFloat+Round block must be gone.
	// We detect it by the characteristic three-line pattern:
	// strconv.ParseFloat(...) immediately followed by math.Round(... * 100)
	if strings.Contains(string(src), "strconv.ParseFloat(hourlyRateStr") {
		t.Error("resume_edit.go still contains inline strconv.ParseFloat(hourlyRateStr, ...) — use parseDollarsToCents instead")
	}
	if strings.Contains(string(src), "math.Round(rate * 100)") {
		t.Error("resume_edit.go still contains inline math.Round(rate * 100) — use parseDollarsToCents instead")
	}
}

// TestUpwork_NoInlineDollarDisplay_Fitness is a source-level fitness assertion.
// After the refactor, upwork.go must NOT contain an inline Sprintf("$%.2f/hr", ...)
// display for HourlyRateCents in buildUpworkPageData; it must use centsToDollars.
//
// Red-on-revert: restore the inline fmt.Sprintf("$%.2f/hr", ...) → test fails.
func TestUpwork_NoInlineDollarDisplay_Fitness(t *testing.T) {
	src, err := os.ReadFile("upwork.go")
	if err != nil {
		t.Fatalf("read upwork.go: %v", err)
	}
	if strings.Contains(string(src), "Sprintf(\"$%.2f/hr\"") {
		t.Errorf("upwork.go still contains inline fmt.Sprintf dollar-2f/hr display — use centsToDollars()+\"/hr\" instead")
	}
}
