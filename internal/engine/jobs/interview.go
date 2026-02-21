package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// --- Interview Preparation ---

// InterviewQuestion is a single Q&A item for interview prep.
type InterviewQuestion struct {
	Category    string `json:"category"`     // behavioral, technical, system_design
	Question    string `json:"question"`
	WhyAsked    string `json:"why_asked"`    // why this is relevant for the role
	ModelAnswer string `json:"model_answer"` // answer using candidate's actual projects/experience
	Tips        string `json:"tips"`         // delivery tips
}

// InterviewPrepResult is the structured output of interview_prep.
type InterviewPrepResult struct {
	Role      string              `json:"role"`
	Company   string              `json:"company,omitempty"`
	Questions []InterviewQuestion `json:"questions"`
	Pitch     string              `json:"pitch"`   // 30-sec elevator pitch for this role
	Summary   string              `json:"summary"` // overall prep advice
}

const interviewPrepPrompt = `You are an expert interview coach who prepares candidates with personalized questions and model answers grounded in their actual experience.

Analyze the resume and job description below. Generate interview questions with model answers that reference the candidate's REAL projects, metrics, and experience from the resume.

RESUME:
%s

JOB DESCRIPTION:
%s
%s
FOCUS: %s

Generate questions according to the focus:
- "all" or empty: 5 behavioral + 7 technical + 3 system design = 15 questions
- "behavioral": 10 behavioral questions
- "technical": 12 technical questions
- "system_design": 5 system design questions

Rules for model answers:
- Every answer MUST reference specific projects, technologies, or metrics from the resume
- Use the STAR method (Situation, Task, Action, Result) for behavioral answers
- Technical answers should demonstrate depth matching the JD requirements
- System design answers should propose architectures using technologies the candidate actually knows

Return a JSON object with this exact structure:
{
  "role": "<target role title from JD>",
  "company": "<company name if known, otherwise empty string>",
  "questions": [
    {
      "category": "<behavioral|technical|system_design>",
      "question": "<the interview question>",
      "why_asked": "<why this question is relevant for this specific role>",
      "model_answer": "<detailed answer using candidate's actual experience, 3-5 sentences>",
      "tips": "<delivery tip: tone, what to emphasize, common pitfalls>"
    }
  ],
  "pitch": "<30-second elevator pitch tailored to this role, referencing top achievements>",
  "summary": "<2-3 sentences of overall prep advice for this specific interview>"
}

Return ONLY the JSON object, no markdown, no explanation.`

// PrepareInterview generates personalized interview Q&A from resume and job description.
// If company is provided, enriches questions with company research context.
func PrepareInterview(ctx context.Context, resume, jobDescription, company, focus string) (*InterviewPrepResult, error) {
	resumeTrunc := engine.TruncateRunes(resume, 4000, "")
	jdTrunc := engine.TruncateRunes(jobDescription, 3000, "")

	if focus == "" {
		focus = "all"
	}
	validFocus := map[string]bool{"all": true, "behavioral": true, "technical": true, "system_design": true}
	if !validFocus[strings.ToLower(focus)] {
		focus = "all"
	}

	// Optional company enrichment
	var companyContext string
	if company != "" {
		res, err := ResearchCompany(ctx, company)
		if err != nil {
			slog.Warn("interview_prep: company research failed, proceeding without", slog.Any("error", err))
		} else {
			var parts []string
			if len(res.TechStack) > 0 {
				parts = append(parts, "Tech stack: "+strings.Join(res.TechStack, ", "))
			}
			if res.CultureNotes != "" {
				parts = append(parts, "Culture: "+res.CultureNotes)
			}
			if len(res.RecentNews) > 0 {
				parts = append(parts, "Recent news: "+strings.Join(res.RecentNews, "; "))
			}
			if res.Size != "" {
				parts = append(parts, "Size: "+res.Size)
			}
			if res.Industry != "" {
				parts = append(parts, "Industry: "+res.Industry)
			}
			if len(parts) > 0 {
				companyContext = fmt.Sprintf("\nCOMPANY CONTEXT (%s):\n%s\n", company, strings.Join(parts, "\n"))
			}
		}
	}

	prompt := fmt.Sprintf(interviewPrepPrompt, resumeTrunc, jdTrunc, companyContext, focus)
	raw, err := engine.CallLLM(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("interview_prep LLM: %w", err)
	}

	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var result InterviewPrepResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("interview_prep parse: %w (raw: %s)", err, engine.TruncateRunes(raw, 200, "..."))
	}
	return &result, nil
}
