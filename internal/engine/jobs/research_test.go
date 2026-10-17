package jobs

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/anatolykoptev/go_job/internal/engine"
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
// When engine.Init is NOT called, searchWeb returns empty (SearchDirect guard
// fires: fetcherProxy==nil, RawSearcherInstance==nil). ResearchCompany must
// return an error in this state.
//
// Note: this test cannot directly verify the search routing because ResearchCompany
// calls searchWeb as a package-level function with no injection seam. The live
// probe verifies the end-to-end behavior: gojob_company_research_total{outcome=ok}
// must increment after deploy.
func TestResearchCompany_ErrorsWhenNoSnippets(t *testing.T) {
	// engine.Init is NOT called — package-level vars stay zero/nil.
	// searchWeb returns nil (SearchDirect guard fires, RawSearcherInstance==nil).
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

// TestResearchCompany_ChannelsDrained verifies that the searchWeb goroutines
// are always drained (preventing goroutine leaks) even when the context is already
// cancelled. The channel is sized len(queries); all goroutines MUST send before
// ResearchCompany returns (blocking send on buffered channel). This test goes RED
// if the drain loop ("for range queries { res := <-ch }") is removed from
// ResearchCompany.
func TestResearchCompany_ChannelsDrained(t *testing.T) {
	// Use an already-cancelled context. searchWeb returns immediately (both
	// RawSearcherInstance==nil and SearchDirect guard fires). All goroutines
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

func TestResearchCompany_UnrelatedSearchResultsReturnsInsufficientData(t *testing.T) {
	origSearch := searchWebFn
	origLLM := callResearchLLM
	t.Cleanup(func() {
		searchWebFn = origSearch
		callResearchLLM = origLLM
	})

	searchWebFn = func(_ context.Context, _ string) []engine.SearxngResult {
		return []engine.SearxngResult{
			{
				Title:   "Top 10 Tech Startups in 2025",
				URL:     "https://techcrunch.com/2025/01/startups",
				Content: "Acme Corp raised $50M in Series B funding with 200 employees.",
			},
		}
	}
	llmCalled := false
	callResearchLLM = func(_ context.Context, _ string) (string, error) {
		llmCalled = true
		return `{}`, nil
	}

	res, err := ResearchCompany(context.Background(), "Mayatin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if llmCalled {
		t.Fatal("expected LLM not to be called when no search result mentions target company")
	}
	if res.Matched {
		t.Fatalf("expected Matched=false for unrelated search results, got %+v", res)
	}
	if res.Confidence != "none" {
		t.Fatalf("expected Confidence=\"none\", got %q", res.Confidence)
	}
	if !strings.Contains(strings.ToLower(res.Summary), "insufficient data") {
		t.Fatalf("expected Insufficient data summary, got %q", res.Summary)
	}
	if BuildCompanyContext("Mayatin", res) != "" {
		t.Fatalf("expected BuildCompanyContext to return empty string for unmatched company, got %q", BuildCompanyContext("Mayatin", res))
	}
}

func TestResearchCompany_StripsPlaceholdersAndMarksInsufficient(t *testing.T) {
	origSearch := searchWebFn
	origLLM := callResearchLLM
	t.Cleanup(func() {
		searchWebFn = origSearch
		callResearchLLM = origLLM
	})

	searchWebFn = func(_ context.Context, _ string) []engine.SearxngResult {
		return []engine.SearxngResult{
			{
				Title:   "Mayatin Directory Listing",
				URL:     "https://example.com/mayatin",
				Content: "Mayatin is listed in the directory.",
			},
		}
	}
	// Simulate LLM hallucinating template placeholders ("Public (NASDAQ: XYZ)", "N/A", "<primary industry>")
	callResearchLLM = func(_ context.Context, _ string) (string, error) {
		return `{
			"name": "Mayatin",
			"matched": true,
			"confidence": "low",
			"size": "unknown",
			"founded": "N/A",
			"industry": "<primary industry>",
			"funding": "Public (NASDAQ: XYZ)",
			"tech_stack": ["unknown", "<tech>"],
			"culture_notes": "not available",
			"recent_news": ["N/A"],
			"glassdoor_rating": 4.2,
			"website": "not disclosed",
			"summary": "Generic company profile."
		}`, nil
	}

	res, err := ResearchCompany(context.Background(), "Mayatin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Matched {
		t.Fatalf("expected Matched=false when all fields are placeholders, got %+v", res)
	}
	if res.Confidence != "none" {
		t.Fatalf("expected Confidence=\"none\", got %q", res.Confidence)
	}
	if res.Funding != "" || res.Industry != "" || res.Size != "" || res.GlassdoorRating != 0 {
		t.Fatalf("expected all placeholder fields to be cleared, got %+v", res)
	}
	if !strings.Contains(strings.ToLower(res.Summary), "insufficient data") {
		t.Fatalf("expected Insufficient data summary, got %q", res.Summary)
	}
}

func TestResearchCompany_VerifiedNonITCompany(t *testing.T) {
	origSearch := searchWebFn
	origLLM := callResearchLLM
	t.Cleanup(func() {
		searchWebFn = origSearch
		callResearchLLM = origLLM
	})

	searchWebFn = func(_ context.Context, _ string) []engine.SearxngResult {
		return []engine.SearxngResult{
			{
				Title:   "Клиника доктора Маятина — mayatin.ru",
				URL:     "https://mayatin.ru",
				Content: "Mayatin medical clinic providing healthcare and diagnostics services.",
			},
		}
	}
	callResearchLLM = func(_ context.Context, _ string) (string, error) {
		return `{
			"name": "Mayatin Clinic",
			"matched": true,
			"confidence": "high",
			"size": "",
			"founded": "",
			"industry": "Healthcare / Medical Clinic",
			"funding": "Public (NASDAQ: XYZ)",
			"tech_stack": [],
			"culture_notes": "",
			"recent_news": [],
			"glassdoor_rating": 0,
			"website": "https://mayatin.ru",
			"summary": "Mayatin is a Russian medical clinic (mayatin.ru) providing healthcare services."
		}`, nil
	}

	res, err := ResearchCompany(context.Background(), "Mayatin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Matched {
		t.Fatalf("expected Matched=true for verified company, got %+v", res)
	}
	if res.Confidence != "high" {
		t.Fatalf("expected Confidence=\"high\", got %q", res.Confidence)
	}
	if res.Industry != "Healthcare / Medical Clinic" {
		t.Fatalf("expected Industry=\"Healthcare / Medical Clinic\", got %q", res.Industry)
	}
	if res.Funding != "" {
		t.Fatalf("expected placeholder funding 'Public (NASDAQ: XYZ)' to be stripped, got %q", res.Funding)
	}
	if len(res.Sources) != 1 || res.Sources[0] != "https://mayatin.ru" {
		t.Fatalf("expected Sources=[https://mayatin.ru], got %v", res.Sources)
	}
	ctxStr := BuildCompanyContext("Mayatin", res)
	if !strings.Contains(ctxStr, "Healthcare / Medical Clinic") {
		t.Fatalf("expected BuildCompanyContext to include verified industry, got %q", ctxStr)
	}
}

func TestResearchSalary_SuccessAndErrorPaths(t *testing.T) {
	origSearch := searchWebFn
	origLLM := callResearchLLM
	t.Cleanup(func() {
		searchWebFn = origSearch
		callResearchLLM = origLLM
	})

	searchWebFn = func(_ context.Context, _ string) []engine.SearxngResult {
		return []engine.SearxngResult{
			{
				Title:   "Go Developer Salary Remote",
				URL:     "https://levels.fyi/salaries/go",
				Content: "Median salary for Remote Go Developer is $155,000.",
			},
		}
	}
	callResearchLLM = func(_ context.Context, _ string) (string, error) {
		return "```json\n{\"role\":\"Go Developer\",\"location\":\"Remote\",\"currency\":\"USD\",\"p25\":130000,\"median\":155000,\"p75\":185000,\"sources\":[\"levels.fyi\"],\"notes\":\"Base salary\",\"updated_at\":\"2025\"}\n```", nil
	}

	sal, err := ResearchSalary(context.Background(), "Go Developer", "Remote", "senior")
	if err != nil {
		t.Fatalf("ResearchSalary unexpected error: %v", err)
	}
	if sal.Median != 155000 || sal.Currency != "USD" {
		t.Fatalf("unexpected salary result: %+v", sal)
	}

	// Verify LLM error propagation
	callResearchLLM = func(_ context.Context, _ string) (string, error) {
		return "", errors.New("upstream llm failure")
	}
	if _, err := ResearchSalary(context.Background(), "Go Developer", "Remote", "senior"); err == nil {
		t.Fatal("expected ResearchSalary to return error on LLM failure")
	}
}

