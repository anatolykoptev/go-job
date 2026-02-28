package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// SkillGapItem describes a single missing skill with learning guidance.
type SkillGapItem struct {
	Skill        string `json:"skill"`
	Category     string `json:"category"`
	Priority     string `json:"priority"`
	LearningTime string `json:"learning_time"`
	Suggestion   string `json:"suggestion"`
}

// SkillGapResult is the structured output of skill gap analysis.
type SkillGapResult struct {
	MatchScore     float64        `json:"match_score"`
	MatchingSkills []string       `json:"matching_skills"`
	MissingSkills  []SkillGapItem `json:"missing_skills"`
	LearningPlan   string         `json:"learning_plan"`
	Summary        string         `json:"summary"`
}

const skillGapPrompt = `You are a career advisor analyzing skill gaps between a candidate and a job.

RESUME:
%s

JOB DESCRIPTION:
%s

COMPUTED MATCH SCORE: %.1f
MATCHING KEYWORDS: %s
MISSING KEYWORDS: %s

For each missing keyword above, provide a detailed skill gap analysis:

1. Categorize each missing skill:
   - "language" (programming languages)
   - "framework" (libraries, frameworks, platforms)
   - "devops" (infrastructure, CI/CD, cloud, containers)
   - "soft_skill" (communication, leadership, collaboration)
   - "domain" (industry knowledge, domain expertise)

2. Prioritize each skill:
   - "critical" — explicitly required, mentioned multiple times or in requirements section
   - "high" — mentioned in the JD, clearly important for the role
   - "medium" — nice-to-have or implied by other requirements

3. Estimate realistic learning time (e.g. "2-4 weeks", "1-3 months", "3-6 months")

4. Suggest how to learn or demonstrate each skill (courses, projects, certifications, etc.)

5. Create a prioritized learning plan roadmap — order skills by priority (critical first), then by learning time (quick wins first within same priority). Write 2-4 sentences describing the recommended learning path.

6. Write a brief summary (2-3 sentences) of the candidate's overall fit and the most important gaps to address.

Return a JSON object with this exact structure:
{
  "match_score": <echo back the computed match score>,
  "matching_skills": <echo back the matching keywords as an array>,
  "missing_skills": [
    {
      "skill": "<skill name>",
      "category": "<language|framework|devops|soft_skill|domain>",
      "priority": "<critical|high|medium>",
      "learning_time": "<estimated time>",
      "suggestion": "<how to learn or demonstrate this skill>"
    }
  ],
  "learning_plan": "<prioritized learning roadmap>",
  "summary": "<overall fit assessment and key gaps>"
}

Return ONLY the JSON object, no markdown, no explanation.`

// AnalyzeSkillGap analyzes skill gaps between a resume and job description.
func AnalyzeSkillGap(ctx context.Context, resume, jobDescription string) (*SkillGapResult, error) {
	resumeTrunc := engine.TruncateRunes(resume, 4000, "")
	jdTrunc := engine.TruncateRunes(jobDescription, 3000, "")

	resumeKW := ExtractResumeKeywords(resume)
	score, matching, missing := ScoreJobMatch(resumeKW, jobDescription)

	prompt := fmt.Sprintf(skillGapPrompt,
		resumeTrunc, jdTrunc, score,
		strings.Join(matching, ", "),
		strings.Join(missing, ", "),
	)

	raw, err := engine.CallLLM(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("skill_gap LLM: %w", err)
	}

	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var result SkillGapResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("skill_gap parse: %w (raw: %s)", err, engine.TruncateRunes(raw, 200, "..."))
	}

	// Override with computed values — don't trust LLM for these.
	result.MatchScore = score
	result.MatchingSkills = matching

	return &result, nil
}
