package engine

import (
	"strings"
	"testing"
)

// TestJobSearchInstruction_ATSSourcesEnumerated guards against the 2026-06-24
// regression where the LLM extraction prompt omitted ashby from both the source
// enum and the URL→source mapping, so every jobs.ashbyhq.com listing was
// labelled source:"other" — silently breaking
// gojob_platform_results_total{platform=ashby} attribution even though discovery,
// fetch, and routing all worked. The prompt is the single point where a fetched
// ATS job is mapped to its platform label, so each ATS platform MUST appear in
// both the enum and the URL rule.
func TestJobSearchInstruction_ATSSourcesEnumerated(t *testing.T) {
	for _, want := range []string{`"greenhouse"`, `"lever"`, `"ashby"`} {
		if !strings.Contains(JobSearchInstruction, want) {
			t.Errorf("JobSearchInstruction missing source enum value %s", want)
		}
	}
	// Both greenhouse board hosts must be mapped: legacy boards.greenhouse.io and
	// the migrated job-boards.greenhouse.io (the live host as of 2026-06).
	for _, host := range []string{
		"boards.greenhouse.io",
		"job-boards.greenhouse.io",
		"jobs.lever.co",
		"jobs.ashbyhq.com",
	} {
		if !strings.Contains(JobSearchInstruction, host) {
			t.Errorf("JobSearchInstruction missing URL→source mapping for %s", host)
		}
	}
}

// TestJobSearchInstruction_SemanticMatchRule guards against the regression
// where the prompt told the LLM to match "query keywords" literally against
// title/skills/description, so a "Developer Relations" query dropped every
// "Developer Advocate" / "DevRel" listing and a "Head of Growth" query
// dropped every "Growth Lead" / "VP Growth" listing — the sources returned
// results but the LLM returned an empty jobs array because the exact query
// words were absent. The rule must instruct the LLM to match by role
// family / meaning, explicitly naming synonyms and variant titles.
func TestJobSearchInstruction_SemanticMatchRule(t *testing.T) {
	for _, want := range []string{"MEANING", "role family", "synonyms"} {
		if !strings.Contains(JobSearchInstruction, want) {
			t.Errorf("JobSearchInstruction missing semantic-match concept %q", want)
		}
	}
	// The old literal-keyword phrasing must be gone.
	if strings.Contains(JobSearchInstruction, "query keywords (match against title") {
		t.Error("JobSearchInstruction still uses literal-keyword matching phrasing")
	}
}
