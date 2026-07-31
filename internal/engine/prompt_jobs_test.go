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
	instr := JobSearchInstructionFor(JobSearchMaxLimit)
	for _, want := range []string{`"greenhouse"`, `"lever"`, `"ashby"`} {
		if !strings.Contains(instr, want) {
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
		if !strings.Contains(instr, host) {
			t.Errorf("JobSearchInstruction missing URL→source mapping for %s", host)
		}
	}
}
