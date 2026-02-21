package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// ResumeEnrichResult is the structured output of resume_enrich.
type ResumeEnrichResult struct {
	Status    string           `json:"status"` // "questions", "complete"
	Questions []EnrichQuestion `json:"questions,omitempty"`
	Applied   int              `json:"applied,omitempty"`
	Summary   string           `json:"summary"`
}

// EnrichQuestion is a single enrichment question.
type EnrichQuestion struct {
	ID       string `json:"id"`
	Category string `json:"category"` // "missing_metric", "hidden_skill", "role_detail", "project_detail"
	Question string `json:"question"`
	Context  string `json:"context"`
}

const enrichQuestionPrompt = `You are an expert career coach analyzing a resume knowledge graph. Generate insightful questions to fill gaps and uncover hidden strengths.

CURRENT RESUME DATA:
%s

Analyze this data and generate 5-10 questions grouped by category. Focus on:
1. "missing_metric" — achievements or experiences that lack quantified metrics (numbers, percentages, dollar amounts)
2. "hidden_skill" — experiences that likely involved skills not yet captured (e.g., managing a team implies leadership, project management)
3. "role_detail" — roles with vague descriptions that could be enriched with specifics
4. "project_detail" — projects or sub-projects that need more detail about outcomes or technologies

Return a JSON object:
{
  "questions": [
    {
      "id": "q1",
      "category": "missing_metric",
      "question": "For your role at Company X, you mentioned growing the user base. Can you quantify that? (e.g., from X to Y users, percentage growth)",
      "context": "Experience: Title at Company"
    }
  ]
}

Generate questions that would produce HIGH-VALUE enrichments for ATS matching. Skip trivial questions.
Return ONLY the JSON object, no markdown, no explanation.`

const enrichApplyPrompt = `You are a resume enrichment engine. Given the current resume data and user answers to enrichment questions, determine what specific updates should be made.

CURRENT RESUME DATA:
%s

QUESTIONS AND ANSWERS:
%s

For each answer, determine what updates to make. Return a JSON object:
{
  "updates": [
    {
      "type": "add_skill",
      "name": "Team Leadership",
      "category": "soft_skill",
      "level": "advanced",
      "source": "enrichment"
    },
    {
      "type": "update_achievement",
      "achievement_text": "original achievement text to match",
      "metric_numeric": 16000,
      "metric_unit": "tickets",
      "new_text": "Sold 16,000 tickets with zero marketing budget through guerrilla marketing tactics"
    },
    {
      "type": "add_project",
      "parent_experience": "company name to match",
      "name": "Project Name",
      "description": "...",
      "tech": ["tech1"],
      "highlights": ["highlight1"]
    },
    {
      "type": "add_methodology",
      "name": "Methodology Name",
      "description": "Brief description"
    },
    {
      "type": "add_domain",
      "name": "Domain Name"
    }
  ]
}

Only include updates that are clearly supported by the user's answers. Do not fabricate information.
Return ONLY the JSON object, no markdown, no explanation.`

// EnrichResume handles the interactive enrichment flow.
func EnrichResume(ctx context.Context, action string, answers []AnswerPair) (*ResumeEnrichResult, error) {
	db := GetResumeDB()
	if db == nil {
		return nil, errors.New("resume database not configured (set DATABASE_URL)")
	}

	personID := db.GetLatestPersonID(ctx)
	if personID == 0 {
		return nil, errors.New("no master resume found — run master_resume_build first")
	}

	switch action {
	case "start":
		return enrichStart(ctx, db, personID)
	case "answer":
		return enrichAnswer(ctx, db, personID, answers)
	default:
		return nil, fmt.Errorf("invalid action %q — use 'start' or 'answer'", action)
	}
}

// AnswerPair holds a question ID and its answer.
type AnswerPair struct {
	QuestionID string `json:"question_id"`
	Answer     string `json:"answer"`
}

func enrichStart(ctx context.Context, db *ResumeDB, personID int) (*ResumeEnrichResult, error) {
	// Load current data
	dataStr := buildCurrentDataString(ctx, db, personID)

	prompt := fmt.Sprintf(enrichQuestionPrompt, engine.TruncateRunes(dataStr, 8000, ""))
	raw, err := engine.CallLLM(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("enrich start LLM: %w", err)
	}

	raw = stripMarkdownFences(raw)

	var parsed struct {
		Questions []EnrichQuestion `json:"questions"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("enrich start parse: %w (raw: %s)", err, engine.TruncateRunes(raw, 200, "..."))
	}

	return &ResumeEnrichResult{
		Status:    "questions",
		Questions: parsed.Questions,
		Summary:   fmt.Sprintf("Generated %d enrichment questions across categories.", len(parsed.Questions)),
	}, nil
}

func enrichAnswer(ctx context.Context, db *ResumeDB, personID int, answers []AnswerPair) (*ResumeEnrichResult, error) {
	if len(answers) == 0 {
		return nil, errors.New("no answers provided")
	}

	// Load current data
	dataStr := buildCurrentDataString(ctx, db, personID)

	// Format Q&A
	var qaStr strings.Builder
	for _, a := range answers {
		fmt.Fprintf(&qaStr, "Question %s: %s\n", a.QuestionID, a.Answer)
	}

	prompt := fmt.Sprintf(enrichApplyPrompt,
		engine.TruncateRunes(dataStr, 6000, ""),
		qaStr.String(),
	)
	raw, err := engine.CallLLM(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("enrich answer LLM: %w", err)
	}

	raw = stripMarkdownFences(raw)

	var parsed struct {
		Updates []json.RawMessage `json:"updates"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("enrich answer parse: %w (raw: %s)", err, engine.TruncateRunes(raw, 200, "..."))
	}

	applied := 0
	mdb := GetMemDB()

	for _, updateRaw := range parsed.Updates {
		var base struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(updateRaw, &base); err != nil {
			continue
		}

		switch base.Type {
		case "add_skill":
			var u struct {
				Name     string `json:"name"`
				Category string `json:"category"`
				Level    string `json:"level"`
				Source   string `json:"source"`
			}
			if err := json.Unmarshal(updateRaw, &u); err != nil {
				continue
			}
			sid, err := db.InsertSkillExtended(ctx, personID, SkillRecord{
				Name:       u.Name,
				Category:   u.Category,
				Level:      u.Level,
				IsImplicit: true,
				Source:     "enrichment",
			})
			if err == nil {
				if err := db.UpsertGraphNode(ctx, "Skill", sid, map[string]string{"name": u.Name}); err != nil {
					slog.Debug("graph node upsert failed", slog.Any("error", err))
				}
				applied++
			}

		case "update_achievement":
			var u struct {
				AchievementText string   `json:"achievement_text"`
				MetricNumeric   *float64 `json:"metric_numeric"`
				MetricUnit      string   `json:"metric_unit"`
				NewText         string   `json:"new_text"`
			}
			if err := json.Unmarshal(updateRaw, &u); err != nil {
				continue
			}
			// Find matching achievement and update
			achvs, _ := db.GetAllAchievements(ctx, personID)
			for _, a := range achvs {
				if strings.Contains(strings.ToLower(a.Text), strings.ToLower(u.AchievementText)) ||
					strings.Contains(strings.ToLower(u.AchievementText), strings.ToLower(a.Text)) {
					updateAchievementMetrics(ctx, db, a.ID, u.MetricNumeric, u.MetricUnit, u.NewText)
					applied++
					break
				}
			}

		case "add_project":
			var u struct {
				ParentExperience string   `json:"parent_experience"`
				Name             string   `json:"name"`
				Description      string   `json:"description"`
				Tech             []string `json:"tech"`
				Highlights       []string `json:"highlights"`
			}
			if err := json.Unmarshal(updateRaw, &u); err != nil {
				continue
			}
			var parentPtr *int
			if u.ParentExperience != "" {
				exps, _ := db.GetAllExperiences(ctx, personID)
				for _, exp := range exps {
					if strings.EqualFold(exp.Company, u.ParentExperience) {
						parentPtr = &exp.ID
						break
					}
				}
			}
			projID, err := db.InsertProjectWithParent(ctx, personID, parentPtr, ProjectRecord{
				Name:        u.Name,
				Description: u.Description,
				Tech:        u.Tech,
				Highlights:  u.Highlights,
			})
			if err == nil {
				if err := db.UpsertGraphNode(ctx, "Proj", projID, map[string]string{"name": u.Name}); err != nil {
					slog.Debug("graph node upsert failed", slog.Any("error", err))
				}
				if parentPtr != nil {
					if err := db.UpsertGraphEdge(ctx, "Proj", projID, "PART_OF", "Exp", *parentPtr); err != nil {
						slog.Debug("graph edge upsert failed", slog.Any("error", err))
					}
				}
				// Add to MemDB
				if mdb != nil {
					text := formatProjectText(u.Name, u.Description, u.Tech, u.Highlights)
					if err := mdb.Add(ctx, text, map[string]any{"type": "project", "id": float64(projID)}); err != nil {
						slog.Debug("memdb add project failed", slog.Any("error", err))
					}
				}
				applied++
			}

		case "add_methodology":
			var u struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			}
			if err := json.Unmarshal(updateRaw, &u); err != nil {
				continue
			}
			methID, err := db.InsertMethodology(ctx, personID, u.Name, u.Description)
			if err == nil {
				if err := db.UpsertGraphNode(ctx, "Method", methID, map[string]string{"name": u.Name}); err != nil {
					slog.Debug("graph node upsert failed", slog.Any("error", err))
				}
				applied++
			}

		case "add_domain":
			var u struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(updateRaw, &u); err != nil {
				continue
			}
			domID, err := db.InsertDomain(ctx, personID, u.Name)
			if err == nil {
				if err := db.UpsertGraphNode(ctx, "Domain", domID, map[string]string{"name": u.Name}); err != nil {
					slog.Debug("upsert domain graph node failed", slog.Any("error", err))
				}
				applied++
			}
		}
	}

	// Mark as enriched
	if err := db.MarkPersonEnriched(ctx, personID); err != nil {
		slog.Debug("mark person enriched failed", slog.Any("error", err))
	}

	slog.Info("enrichment applied", slog.Int("person_id", personID), slog.Int("applied", applied))

	return &ResumeEnrichResult{
		Status:  "complete",
		Applied: applied,
		Summary: fmt.Sprintf("Applied %d enrichments from %d answers.", applied, len(answers)),
	}, nil
}

// updateAchievementMetrics updates metric fields on an achievement.
func updateAchievementMetrics(ctx context.Context, db *ResumeDB, achvID int, metricNumeric *float64, metricUnit, newText string) {
	if newText != "" {
		if _, err := db.pool.Exec(ctx,
			`UPDATE resume_achievements SET text = $2 WHERE id = $1`, achvID, newText); err != nil {
			slog.Debug("update achievement text failed", slog.Any("error", err))
		}
	}
	if metricNumeric != nil || metricUnit != "" {
		if _, err := db.pool.Exec(ctx,
			`UPDATE resume_achievements SET metric_numeric = $2, metric_unit = $3 WHERE id = $1`,
			achvID, metricNumeric, metricUnit); err != nil {
			slog.Debug("update achievement metrics failed", slog.Any("error", err))
		}
	}
}

// buildCurrentDataString assembles current resume data for LLM consumption.
func buildCurrentDataString(ctx context.Context, db *ResumeDB, personID int) string {
	var b strings.Builder

	exps, _ := db.GetAllExperiences(ctx, personID)
	b.WriteString("EXPERIENCES:\n")
	for _, e := range exps {
		fmt.Fprintf(&b, "- %s at %s (%s-%s)", e.Title, e.Company, e.StartDate, e.EndDate)
		if e.Domain != "" {
			fmt.Fprintf(&b, " [%s]", e.Domain)
		}
		b.WriteString("\n")
		if e.Description != "" {
			fmt.Fprintf(&b, "  %s\n", e.Description)
		}
		for _, h := range e.Highlights {
			fmt.Fprintf(&b, "  * %s\n", h)
		}
	}

	skills, _ := db.GetAllSkills(ctx, personID)
	b.WriteString("\nSKILLS:\n")
	for _, s := range skills {
		label := s.Name
		if s.IsImplicit {
			label += " (inferred)"
		}
		fmt.Fprintf(&b, "- %s [%s, %s]\n", label, s.Category, s.Level)
	}

	projs, _ := db.GetAllProjects(ctx, personID)
	if len(projs) > 0 {
		b.WriteString("\nPROJECTS:\n")
		for _, p := range projs {
			fmt.Fprintf(&b, "- %s: %s\n", p.Name, p.Description)
		}
	}

	achvs, _ := db.GetAllAchievements(ctx, personID)
	if len(achvs) > 0 {
		b.WriteString("\nACHIEVEMENTS:\n")
		for _, a := range achvs {
			fmt.Fprintf(&b, "- %s", a.Text)
			if a.MetricNumeric != nil {
				fmt.Fprintf(&b, " (%.0f %s)", *a.MetricNumeric, a.MetricUnit)
			}
			b.WriteString("\n")
		}
	}

	domains, _ := db.GetAllDomains(ctx, personID)
	if len(domains) > 0 {
		b.WriteString("\nDOMAINS:\n")
		for _, d := range domains {
			fmt.Fprintf(&b, "- %s\n", d.Name)
		}
	}

	meths, _ := db.GetAllMethodologies(ctx, personID)
	if len(meths) > 0 {
		b.WriteString("\nMETHODOLOGIES:\n")
		for _, m := range meths {
			fmt.Fprintf(&b, "- %s: %s\n", m.Name, m.Description)
		}
	}

	return b.String()
}
