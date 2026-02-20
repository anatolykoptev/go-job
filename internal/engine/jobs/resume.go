package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// --- Resume Analysis ---

// ResumeAnalysisResult is the structured output of resume_analyze.
type ResumeAnalysisResult struct {
	ATSScore         int      `json:"ats_score"`
	MatchingKeywords []string `json:"matching_keywords"`
	MissingKeywords  []string `json:"missing_keywords"`
	Gaps             []string `json:"gaps"`
	Recommendations  []string `json:"recommendations"`
	Summary          string   `json:"summary"`
}

const resumeAnalyzePrompt = `You are an expert ATS (Applicant Tracking System) resume analyst.

Analyze the following resume against the job description and return a JSON object with this exact structure:
{
  "ats_score": <integer 0-100, how well the resume matches the JD>,
  "matching_keywords": [<keywords present in both resume and JD>],
  "missing_keywords": [<important keywords from JD missing in resume>],
  "gaps": [<experience/skills/qualifications required by JD but absent in resume>],
  "recommendations": [<specific actionable improvements, max 5>],
  "summary": "<2-3 sentence overall assessment>"
}

RESUME:
%s

JOB DESCRIPTION:
%s

Return ONLY the JSON object, no markdown, no explanation.`

// AnalyzeResume compares a resume against a job description and returns ATS analysis.
func AnalyzeResume(ctx context.Context, resumeText, jobDescription string) (*ResumeAnalysisResult, error) {
	resumeTrunc := engine.TruncateRunes(resumeText, 4000, "")
	jdTrunc := engine.TruncateRunes(jobDescription, 3000, "")

	prompt := fmt.Sprintf(resumeAnalyzePrompt, resumeTrunc, jdTrunc)
	raw, err := engine.CallLLM(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("resume_analyze LLM: %w", err)
	}

	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var result ResumeAnalysisResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("resume_analyze parse: %w (raw: %s)", err, engine.TruncateRunes(raw, 200, "..."))
	}
	return &result, nil
}

// --- Cover Letter Generation ---

// CoverLetterResult is the structured output of cover_letter_generate.
type CoverLetterResult struct {
	CoverLetter string `json:"cover_letter"`
	WordCount   int    `json:"word_count"`
	Tone        string `json:"tone"`
}

const coverLetterPrompt = `You are an expert career coach and professional writer.

Write a tailored cover letter for the following job application.

Tone: %s
Guidelines:
- 3-4 paragraphs, ~250-350 words
- Opening: express genuine interest, mention company/role by name
- Body: highlight 2-3 most relevant experiences/skills that match the JD
- Closing: call to action, express enthusiasm
- Do NOT use generic phrases like "I am writing to apply for..."
- Use specific details from the resume and JD

RESUME:
%s

JOB DESCRIPTION:
%s

Return ONLY the cover letter text, no JSON, no markdown headers.`

// GenerateCoverLetter creates a tailored cover letter from resume and job description.
// tone: "professional" (default), "friendly", "concise"
func GenerateCoverLetter(ctx context.Context, resumeText, jobDescription, tone string) (*CoverLetterResult, error) {
	if tone == "" {
		tone = "professional"
	}
	validTones := map[string]bool{"professional": true, "friendly": true, "concise": true}
	if !validTones[strings.ToLower(tone)] {
		tone = "professional"
	}

	resumeTrunc := engine.TruncateRunes(resumeText, 3000, "")
	jdTrunc := engine.TruncateRunes(jobDescription, 2000, "")

	prompt := fmt.Sprintf(coverLetterPrompt, tone, resumeTrunc, jdTrunc)
	raw, err := engine.CallLLM(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("cover_letter_generate LLM: %w", err)
	}

	letter := strings.TrimSpace(raw)
	wordCount := len(strings.Fields(letter))

	return &CoverLetterResult{
		CoverLetter: letter,
		WordCount:   wordCount,
		Tone:        tone,
	}, nil
}

// --- Resume Tailoring ---

// ResumeTailorResult is the structured output of resume_tailor.
type ResumeTailorResult struct {
	TailoredSections map[string]string `json:"tailored_sections"`
	AddedKeywords    []string          `json:"added_keywords"`
	RemovedKeywords  []string          `json:"removed_keywords"`
	DiffSummary      string            `json:"diff_summary"`
	TailoredResume   string            `json:"tailored_resume"`
}

const resumeTailorPrompt = `You are an expert resume writer and ATS optimization specialist.

Rewrite the resume to better match the job description. Focus on:
1. Incorporating missing keywords naturally
2. Reordering bullet points to highlight most relevant experience first
3. Quantifying achievements where possible
4. Matching the terminology used in the JD

Return a JSON object with this exact structure:
{
  "tailored_sections": {
    "<section_name>": "<rewritten section content>"
  },
  "added_keywords": [<keywords added to the resume>],
  "removed_keywords": [<keywords removed or de-emphasized>],
  "diff_summary": "<2-3 sentences describing the main changes made>",
  "tailored_resume": "<complete rewritten resume text>"
}

ORIGINAL RESUME:
%s

JOB DESCRIPTION:
%s

Return ONLY the JSON object, no markdown, no explanation.`

// TailorResume rewrites resume sections to better match a specific job description.
func TailorResume(ctx context.Context, resumeText, jobDescription string) (*ResumeTailorResult, error) {
	resumeTrunc := engine.TruncateRunes(resumeText, 4000, "")
	jdTrunc := engine.TruncateRunes(jobDescription, 3000, "")

	prompt := fmt.Sprintf(resumeTailorPrompt, resumeTrunc, jdTrunc)
	raw, err := engine.CallLLM(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("resume_tailor LLM: %w", err)
	}

	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var result ResumeTailorResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("resume_tailor parse: %w (raw: %s)", err, engine.TruncateRunes(raw, 200, "..."))
	}
	return &result, nil
}
