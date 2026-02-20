package jobs

import (
	"encoding/json"
	"strings"
	"testing"
)

// --- GenerateCoverLetter tone normalization ---

func TestCoverLetterToneNormalization(t *testing.T) {
	tests := []struct {
		input    string
		wantTone string
	}{
		{"professional", "professional"},
		{"friendly", "friendly"},
		{"concise", "concise"},
		{"", "professional"},           // default
		{"PROFESSIONAL", "professional"}, // case-insensitive
		{"invalid", "professional"},    // unknown → default
		{"casual", "professional"},     // unknown → default
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			tone := tt.input
			if tone == "" {
				tone = "professional"
			}
			validTones := map[string]bool{"professional": true, "friendly": true, "concise": true}
			if !validTones[strings.ToLower(strings.TrimSpace(tone))] {
				tone = "professional"
			} else {
				tone = strings.ToLower(strings.TrimSpace(tone))
			}
			if tone != tt.wantTone {
				t.Errorf("tone(%q) = %q, want %q", tt.input, tone, tt.wantTone)
			}
		})
	}
}

// --- ResumeAnalysisResult JSON marshaling ---

func TestResumeAnalysisResultJSON(t *testing.T) {
	r := ResumeAnalysisResult{
		ATSScore:         78,
		MatchingKeywords: []string{"Go", "Kubernetes", "REST API"},
		MissingKeywords:  []string{"gRPC", "Prometheus"},
		Gaps:             []string{"No distributed systems experience"},
		Recommendations:  []string{"Add gRPC project", "Mention Prometheus"},
		Summary:          "Strong backend match but missing DevOps keywords.",
	}

	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("json.Marshal error: %v", err)
	}

	var decoded ResumeAnalysisResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal error: %v", err)
	}

	if decoded.ATSScore != 78 {
		t.Errorf("ATSScore = %d, want 78", decoded.ATSScore)
	}
	if len(decoded.MatchingKeywords) != 3 {
		t.Errorf("MatchingKeywords len = %d, want 3", len(decoded.MatchingKeywords))
	}
	if len(decoded.MissingKeywords) != 2 {
		t.Errorf("MissingKeywords len = %d, want 2", len(decoded.MissingKeywords))
	}
	if decoded.Summary != r.Summary {
		t.Errorf("Summary = %q, want %q", decoded.Summary, r.Summary)
	}
}

// --- CoverLetterResult JSON marshaling ---

func TestCoverLetterResultJSON(t *testing.T) {
	r := CoverLetterResult{
		CoverLetter: "Dear Hiring Manager,\n\nI am excited to apply...",
		WordCount:   42,
		Tone:        "professional",
	}

	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("json.Marshal error: %v", err)
	}

	var decoded CoverLetterResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal error: %v", err)
	}

	if decoded.WordCount != 42 {
		t.Errorf("WordCount = %d, want 42", decoded.WordCount)
	}
	if decoded.Tone != "professional" {
		t.Errorf("Tone = %q, want professional", decoded.Tone)
	}
	if !strings.Contains(decoded.CoverLetter, "Hiring Manager") {
		t.Error("CoverLetter missing expected content")
	}
}

// --- ResumeTailorResult JSON marshaling ---

func TestResumeTailorResultJSON(t *testing.T) {
	r := ResumeTailorResult{
		TailoredSections: map[string]string{
			"Summary": "Results-driven Go engineer...",
			"Skills":  "Go, gRPC, Kubernetes...",
		},
		AddedKeywords:   []string{"gRPC", "Prometheus"},
		RemovedKeywords: []string{"PHP"},
		DiffSummary:     "Added gRPC and Prometheus to skills.",
		TailoredResume:  "John Doe\nSenior Go Engineer\n...",
	}

	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("json.Marshal error: %v", err)
	}

	var decoded ResumeTailorResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal error: %v", err)
	}

	if len(decoded.TailoredSections) != 2 {
		t.Errorf("TailoredSections len = %d, want 2", len(decoded.TailoredSections))
	}
	if decoded.TailoredSections["Summary"] != "Results-driven Go engineer..." {
		t.Errorf("TailoredSections[Summary] = %q", decoded.TailoredSections["Summary"])
	}
	if len(decoded.AddedKeywords) != 2 {
		t.Errorf("AddedKeywords len = %d, want 2", len(decoded.AddedKeywords))
	}
	if decoded.DiffSummary != r.DiffSummary {
		t.Errorf("DiffSummary = %q", decoded.DiffSummary)
	}
}

// --- resumeAnalyzePrompt format ---

func TestResumeAnalyzePromptFormat(t *testing.T) {
	resume := "John Doe\nSenior Go Developer"
	jd := "We need a Go engineer with Kubernetes experience"

	prompt := resumeAnalyzePrompt
	if !strings.Contains(prompt, "%s") {
		t.Error("resumeAnalyzePrompt should contain percent-s placeholders")
	}

	// Count placeholders
	count := strings.Count(prompt, "%s")
	if count != 2 {
		t.Errorf("resumeAnalyzePrompt has %d %%s placeholders, want 2", count)
	}

	// Verify sprintf works
	formatted := strings.Replace(strings.Replace(prompt, "%s", resume, 1), "%s", jd, 1)
	if !strings.Contains(formatted, resume) {
		t.Error("formatted prompt should contain resume text")
	}
	if !strings.Contains(formatted, jd) {
		t.Error("formatted prompt should contain job description")
	}
}

// --- coverLetterPrompt format ---

func TestCoverLetterPromptFormat(t *testing.T) {
	count := strings.Count(coverLetterPrompt, "%s")
	if count != 3 {
		t.Errorf("coverLetterPrompt has %d %%s placeholders, want 3 (tone, resume, jd)", count)
	}
}

// --- resumeTailorPrompt format ---

func TestResumeTailorPromptFormat(t *testing.T) {
	count := strings.Count(resumeTailorPrompt, "%s")
	if count != 2 {
		t.Errorf("resumeTailorPrompt has %d %%s placeholders, want 2 (resume, jd)", count)
	}
}

// --- JSON parsing of LLM output (simulated) ---

func TestParseResumeAnalysisJSON(t *testing.T) {
	// Simulate what the LLM would return
	raw := `{
		"ats_score": 72,
		"matching_keywords": ["Go", "REST API", "PostgreSQL"],
		"missing_keywords": ["gRPC", "Kubernetes"],
		"gaps": ["No cloud experience"],
		"recommendations": ["Add Kubernetes to skills", "Mention cloud projects"],
		"summary": "Good match for backend role, missing cloud/container skills."
	}`

	var result ResumeAnalysisResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("json.Unmarshal error: %v", err)
	}

	if result.ATSScore != 72 {
		t.Errorf("ATSScore = %d, want 72", result.ATSScore)
	}
	if len(result.MatchingKeywords) != 3 {
		t.Errorf("MatchingKeywords = %v, want 3 items", result.MatchingKeywords)
	}
	if len(result.MissingKeywords) != 2 {
		t.Errorf("MissingKeywords = %v, want 2 items", result.MissingKeywords)
	}
	if len(result.Gaps) != 1 {
		t.Errorf("Gaps = %v, want 1 item", result.Gaps)
	}
	if len(result.Recommendations) != 2 {
		t.Errorf("Recommendations = %v, want 2 items", result.Recommendations)
	}
}

func TestParseResumeTailorJSON(t *testing.T) {
	raw := `{
		"tailored_sections": {
			"Summary": "Experienced Go developer with 5+ years...",
			"Skills": "Go, Kubernetes, gRPC, PostgreSQL"
		},
		"added_keywords": ["Kubernetes", "gRPC"],
		"removed_keywords": ["PHP", "MySQL"],
		"diff_summary": "Added cloud/container skills, removed legacy stack.",
		"tailored_resume": "John Doe\n5+ years Go experience..."
	}`

	var result ResumeTailorResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("json.Unmarshal error: %v", err)
	}

	if len(result.TailoredSections) != 2 {
		t.Errorf("TailoredSections = %v, want 2 items", result.TailoredSections)
	}
	if result.TailoredSections["Summary"] == "" {
		t.Error("TailoredSections[Summary] should not be empty")
	}
	if len(result.AddedKeywords) != 2 {
		t.Errorf("AddedKeywords = %v, want 2 items", result.AddedKeywords)
	}
	if result.DiffSummary == "" {
		t.Error("DiffSummary should not be empty")
	}
	if result.TailoredResume == "" {
		t.Error("TailoredResume should not be empty")
	}
}

// --- ATSScore boundary values ---

func TestATSScoreBoundaries(t *testing.T) {
	tests := []struct {
		raw   string
		score int
	}{
		{`{"ats_score": 0, "matching_keywords": [], "missing_keywords": [], "gaps": [], "recommendations": [], "summary": ""}`, 0},
		{`{"ats_score": 100, "matching_keywords": [], "missing_keywords": [], "gaps": [], "recommendations": [], "summary": ""}`, 100},
		{`{"ats_score": 50, "matching_keywords": [], "missing_keywords": [], "gaps": [], "recommendations": [], "summary": ""}`, 50},
	}
	for _, tt := range tests {
		var r ResumeAnalysisResult
		if err := json.Unmarshal([]byte(tt.raw), &r); err != nil {
			t.Fatalf("Unmarshal error: %v", err)
		}
		if r.ATSScore != tt.score {
			t.Errorf("ATSScore = %d, want %d", r.ATSScore, tt.score)
		}
	}
}
