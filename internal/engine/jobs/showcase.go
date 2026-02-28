package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// --- Project Showcase ---

// ShowcaseProject is a single project transformed into a STAR-format interview narrative.
type ShowcaseProject struct {
	Name          string   `json:"name"`
	Narrative     string   `json:"narrative"`
	TechStack     []string `json:"tech_stack"`
	Impact        string   `json:"impact"`
	TalkingPoints []string `json:"talking_points"`
}

// ProjectShowcaseResult is the structured output of project_showcase.
type ProjectShowcaseResult struct {
	Projects []ShowcaseProject `json:"projects"`
	Summary  string            `json:"summary"`
}

const projectShowcasePrompt = `You are an expert interview coach who transforms raw project descriptions into compelling STAR-format interview narratives.

Analyze the project descriptions below and create structured narratives that a candidate can use to articulate their work in interviews.

PROJECTS:
%s
%s
For each project, produce:
1. **name** — a concise project title
2. **narrative** — a STAR-format story (Situation: context/problem, Task: your responsibility, Action: what you did and how, Result: measurable outcome)
3. **tech_stack** — technologies and tools used
4. **impact** — quantified impact (numbers, percentages, time saved, revenue, users affected). If not explicitly stated, infer reasonable estimates and note they are estimated.
5. **talking_points** — 3-5 bullet points the candidate should emphasize when telling this story in an interview

Return a JSON object with this exact structure:
{
  "projects": [
    {
      "name": "<project title>",
      "narrative": "<STAR-format narrative, 4-6 sentences>",
      "tech_stack": ["<technology1>", "<technology2>"],
      "impact": "<quantified impact statement>",
      "talking_points": ["<point1>", "<point2>", "<point3>"]
    }
  ],
  "summary": "<2-3 sentences of overall advice on presenting these projects in interviews>"
}

Return ONLY the JSON object, no markdown, no explanation.`

// ShowcaseProjects transforms raw project descriptions into STAR-format interview narratives.
func ShowcaseProjects(ctx context.Context, projects, targetRole string) (*ProjectShowcaseResult, error) {
	projectsTrunc := engine.TruncateRunes(projects, 5000, "")

	var roleContext string
	if targetRole != "" {
		roleContext = fmt.Sprintf("TARGET ROLE: %s\nTailor narratives to highlight relevance for this role.\n", targetRole)
	}

	prompt := fmt.Sprintf(projectShowcasePrompt, projectsTrunc, roleContext)
	raw, err := engine.CallLLM(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("project_showcase LLM: %w", err)
	}

	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var result ProjectShowcaseResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("project_showcase parse: %w (raw: %s)", err, engine.TruncateRunes(raw, 200, "..."))
	}
	return &result, nil
}
