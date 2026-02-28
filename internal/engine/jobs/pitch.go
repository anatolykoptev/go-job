package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// --- Pitch Generation ---

// PitchGenerateResult is the structured output of pitch_generate.
type PitchGenerateResult struct {
	ShortPitch   string   `json:"short_pitch"`
	LongPitch    string   `json:"long_pitch"`
	WhyCompany   string   `json:"why_company"`
	KeyStrengths []string `json:"key_strengths"`
	Summary      string   `json:"summary"`
}

const pitchGeneratePrompt = `You are an expert career coach who crafts compelling elevator pitches grounded in the candidate's actual experience.

Analyze the resume below and generate personalized elevator pitches for the target role. Every pitch must reference REAL projects, metrics, technologies, and achievements from the resume — never use generic filler.

RESUME:
%s

TARGET ROLE: %s
%s
Generate:
- short_pitch: A 30-second elevator pitch (3-4 sentences). Open with who you are and your strongest qualification, highlight one key achievement with a metric, and close with what you're looking for.
- long_pitch: A 2-minute expanded pitch. Cover your professional background, 2-3 key achievements with specifics, the technologies/skills you bring, and why you're excited about this type of role.
- why_company: A "Why this company?" answer. If company context is provided, reference their tech stack, culture, recent news, or industry position. If no company context, provide a structured template the candidate can adapt.
- key_strengths: Top 3-5 selling points from the resume that are most relevant to the target role. Each should be a concise bullet (one sentence).
- summary: Brief prep advice — how to deliver the pitches, what to emphasize, and what to avoid.

Rules:
- Every claim in the pitches MUST be traceable to something in the resume
- Use specific numbers, project names, and technologies — not vague statements
- Pitches should sound natural and conversational, not like a resume recitation
- The short pitch should be memorizable; the long pitch should flow as a story

Return a JSON object with this exact structure:
{
  "short_pitch": "<30-second elevator pitch, 3-4 sentences>",
  "long_pitch": "<2-minute expanded pitch, covers background, achievements, what you bring>",
  "why_company": "<Why this company answer, tailored if company context provided>",
  "key_strengths": ["<strength 1>", "<strength 2>", "<strength 3>"],
  "summary": "<brief prep advice>"
}

Return ONLY the JSON object, no markdown, no explanation.`

// GeneratePitch generates personalized elevator pitches from resume for a target role.
// If company is provided, enriches with company research context.
func GeneratePitch(ctx context.Context, resume, targetRole, company string) (*PitchGenerateResult, error) {
	resumeTrunc := engine.TruncateRunes(resume, 4000, "")

	// Optional company enrichment
	var companyContext string
	if company != "" {
		res, err := ResearchCompany(ctx, company)
		if err != nil {
			slog.Warn("pitch_generate: company research failed, proceeding without", slog.Any("error", err))
		} else {
			companyContext = BuildCompanyContext(company, res)
		}
	}

	prompt := fmt.Sprintf(pitchGeneratePrompt, resumeTrunc, targetRole, companyContext)
	raw, err := engine.CallLLM(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("pitch_generate LLM: %w", err)
	}

	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var result PitchGenerateResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("pitch_generate parse: %w (raw: %s)", err, engine.TruncateRunes(raw, 200, "..."))
	}
	return &result, nil
}
