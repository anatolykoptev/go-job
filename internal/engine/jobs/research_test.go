package jobs

import (
	"context"
	"strings"
	"testing"
	"time"
)

// --- ResearchCompanyBounded (optional-enrichment degradation) ---

// TestResearchCompanyBounded_EmptyCompany verifies the bound short-circuits an
// empty company name with no network call and a nil result.
func TestResearchCompanyBounded_EmptyCompany(t *testing.T) {
	got := ResearchCompanyBounded(context.Background(), "", DefaultCompanyResearchTimeout)
	if got != nil {
		t.Fatalf("ResearchCompanyBounded(\"\") = %+v, want nil", got)
	}
}

// TestResearchCompanyBounded_DeadlineDegrades is the regression guard for the
// resume_generate / application_prep timeout class: a company-research substep
// that cannot finish within its bound MUST degrade to nil (proceed without
// company context) rather than block the parent tool. We force the bound to
// fire by passing an already-cancelled parent context, so the worker's
// ResearchCompany call sees a dead context and the select resolves on
// subCtx.Done() — deterministic, no live network dependency.
//
// This exercises the REAL ResearchCompanyBounded function (the shipped code),
// not a copy.
func TestResearchCompanyBounded_DeadlineDegrades(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already-dead parent context

	done := make(chan *CompanyResearchResult, 1)
	go func() {
		done <- ResearchCompanyBounded(ctx, "ComfyUI", 1*time.Millisecond)
	}()

	select {
	case got := <-done:
		if got != nil {
			t.Fatalf("ResearchCompanyBounded with dead ctx = %+v, want nil (graceful degrade)", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ResearchCompanyBounded did not return within 5s — bound did not fire, tool would hang")
	}
}

// --- isRussianLocation ---

func TestIsRussianLocation(t *testing.T) {
	tests := []struct {
		location string
		want     bool
	}{
		{"Москва", true},
		{"москва", true},
		{"Россия", true},
		{"россия", true},
		{"russia", true},
		{"moscow", true},
		{"saint-petersburg", true},
		{"спб", true},
		{"ru", true},
		{"San Francisco", false},
		{"Berlin", false},
		{"Remote", false},
		{"New York", false},
		{"", false},
		{"London", false},
		{"Paris", false},
	}
	for _, tt := range tests {
		t.Run(tt.location, func(t *testing.T) {
			got := isRussianLocation(tt.location)
			if got != tt.want {
				t.Errorf("isRussianLocation(%q) = %v, want %v", tt.location, got, tt.want)
			}
		})
	}
}

// --- buildSalaryQueries ---

func TestBuildSalaryQueries_International(t *testing.T) {
	queries := buildSalaryQueries("Senior Go Developer", "San Francisco", "senior")
	if len(queries) == 0 {
		t.Fatal("expected non-empty queries")
	}
	// Should contain levels.fyi or glassdoor for international
	combined := strings.Join(queries, " ")
	if !strings.Contains(combined, "levels.fyi") && !strings.Contains(combined, "glassdoor") {
		t.Errorf("international queries should reference levels.fyi or glassdoor, got: %v", queries)
	}
	// Should contain the role
	if !strings.Contains(combined, "Go Developer") {
		t.Errorf("queries should contain role, got: %v", queries)
	}
	// Should contain location
	if !strings.Contains(combined, "San Francisco") {
		t.Errorf("queries should contain location, got: %v", queries)
	}
}

func TestBuildSalaryQueries_Russian(t *testing.T) {
	queries := buildSalaryQueries("Backend Developer", "Москва", "mid")
	if len(queries) == 0 {
		t.Fatal("expected non-empty queries")
	}
	combined := strings.Join(queries, " ")
	// Should reference Russian job sites
	if !strings.Contains(combined, "hh.ru") && !strings.Contains(combined, "habr") {
		t.Errorf("Russian queries should reference hh.ru or habr, got: %v", queries)
	}
}

func TestBuildSalaryQueries_WithExperience(t *testing.T) {
	queries := buildSalaryQueries("Data Engineer", "Remote", "junior")
	combined := strings.Join(queries, " ")
	// Experience level should be included in queries
	if !strings.Contains(combined, "junior") {
		t.Errorf("queries should contain experience level, got: %v", queries)
	}
}

func TestBuildSalaryQueries_NoExperience(t *testing.T) {
	queries := buildSalaryQueries("Product Manager", "Berlin", "")
	if len(queries) == 0 {
		t.Fatal("expected non-empty queries even without experience")
	}
	combined := strings.Join(queries, " ")
	if !strings.Contains(combined, "Product Manager") {
		t.Errorf("queries should contain role, got: %v", queries)
	}
}

func TestBuildSalaryQueries_Count(t *testing.T) {
	// Should return exactly 3 queries for both international and Russian
	intlQueries := buildSalaryQueries("Go Developer", "New York", "senior")
	if len(intlQueries) != 3 {
		t.Errorf("expected 3 international queries, got %d", len(intlQueries))
	}

	ruQueries := buildSalaryQueries("Go Developer", "Москва", "senior")
	if len(ruQueries) != 3 {
		t.Errorf("expected 3 Russian queries, got %d", len(ruQueries))
	}
}

func TestBuildSalaryQueries_RussianLocation_Variants(t *testing.T) {
	ruLocations := []string{"Москва", "россия", "Russia", "Moscow", "saint-petersburg", "спб"}
	for _, loc := range ruLocations {
		queries := buildSalaryQueries("Developer", loc, "")
		combined := strings.Join(queries, " ")
		if !strings.Contains(combined, "hh.ru") && !strings.Contains(combined, "habr") {
			t.Errorf("location %q should produce RU queries with hh.ru/habr, got: %v", loc, queries)
		}
	}
}

// --- pitch_generate + interview_prep degradation guard ---

// TestBuildCompanyContext_NilResearchYieldsEmpty verifies that BuildCompanyContext
// returns an empty string when the CompanyResearchResult is nil. This is the
// nil-degrade contract that pitch_generate (GeneratePitch) and interview_prep
// (PrepareInterview) rely on: ResearchCompanyBounded returns nil on timeout or
// error, and an empty companyContext propagates harmlessly into the prompt.
//
// The test goes RED (panic) if the nil guard in BuildCompanyContext is removed.
func TestBuildCompanyContext_NilResearchYieldsEmpty(t *testing.T) {
	got := BuildCompanyContext("ComfyUI", nil)
	if got != "" {
		t.Fatalf("BuildCompanyContext(%q, nil) = %q, want empty string", "ComfyUI", got)
	}

	// Also guard the empty-company-name path.
	got2 := BuildCompanyContext("", nil)
	if got2 != "" {
		t.Fatalf("BuildCompanyContext(\"\", nil) = %q, want empty string", got2)
	}
}

// --- ResearchCompany error-on-empty guard ---

// TestResearchCompany_ErrorsWhenNoSnippets verifies that ResearchCompany returns a
// non-nil error when all search backends return empty results. This guards the
// "no results found" invariant — the function must never silently return a nil
// result alongside a nil error when it has nothing to synthesize.
//
// When engine.Init is NOT called, both SearchDirect (no scrapers enabled,
// fetcherProxy==nil guard fires) and SearchSearXNG (searxngInst==nil) return empty.
// ResearchCompany must return an error in this state.
//
// Note: this test cannot directly verify that SearchDirect is the PRIMARY search path
// (vs SearchSearXNG) because ResearchCompany calls engine.SearchDirect as a
// package-level function with no injection seam. The routing change is covered by the
// structural change in research.go (SearchDirect fan-out runs unconditionally;
// SearXNG fan-out runs additively). The live probe verifies the end-to-end behavior:
// gojob_company_research_total{outcome=ok} must increment after deploy.
func TestResearchCompany_ErrorsWhenNoSnippets(t *testing.T) {
	// engine.Init is NOT called — package-level vars stay zero/nil.
	// SearchDirect returns nil (fetcherProxy==nil guard fires in directSearchConfig).
	// SearchSearXNG returns nil, nil (searxngInst==nil).
	// All allSnippets channels stay empty → error path.
	ctx := context.Background()
	res, err := ResearchCompany(ctx, "SomeCompanyThatNeverExists12345XYZ")
	if err == nil {
		t.Fatalf("ResearchCompany with no search backend: got result=%+v, want error", res)
	}
	if res != nil {
		t.Fatalf("ResearchCompany with no search backend: got non-nil result=%+v alongside error, want nil", res)
	}
}

// TestResearchCompany_SearXNGChannelsDrained verifies that the SearXNG goroutines
// are always drained (preventing goroutine leaks) even when the context is already
// cancelled. The searxCh channel is sized len(queries); all goroutines MUST send
// before ResearchCompany returns (blocking send on buffered channel, bounded by
// context cancellation in the SearXNG client). This test goes RED if the final
// drain loop ("for range queries { res := <-searxCh }") is removed from ResearchCompany.
func TestResearchCompany_SearXNGChannelsDrained(t *testing.T) {
	// Use an already-cancelled context. SearchSearXNG (searxngInst==nil) returns
	// immediately. SearchDirect guard fires (fetcherProxy==nil). All goroutines
	// send and exit. ResearchCompany must return promptly, not block.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan error, 1)
	go func() {
		_, err := ResearchCompany(ctx, "AnyCompany")
		done <- err
	}()

	select {
	case err := <-done:
		// We expect an error (no snippets), but NOT a hang.
		if err == nil {
			t.Fatal("ResearchCompany with cancelled ctx and no backends: want error, got nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ResearchCompany blocked > 5s — goroutine leak or channel not drained")
	}
}
