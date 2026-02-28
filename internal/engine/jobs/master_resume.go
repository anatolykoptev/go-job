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

// MasterResumeBuildResult is the structured output of master_resume_build.
type MasterResumeBuildResult struct {
	PersonID       int    `json:"person_id"`
	Experiences    int    `json:"experiences"`
	Skills         int    `json:"skills"`
	Projects       int    `json:"projects"`
	Achievements   int    `json:"achievements"`
	Educations     int    `json:"educations"`
	Certifications int    `json:"certifications"`
	Domains        int    `json:"domains"`
	Methodologies  int    `json:"methodologies"`
	ImplicitSkills int    `json:"implicit_skills"`
	SubProjects    int    `json:"sub_projects"`
	GraphNodes     int    `json:"graph_nodes"`
	GraphEdges     int    `json:"graph_edges"`
	VectorsStored  int    `json:"vectors_stored"`
	Summary        string `json:"summary"`
}

type parsedResume struct {
	Person struct {
		Name     string            `json:"name"`
		Email    string            `json:"email"`
		Phone    string            `json:"phone"`
		Location string            `json:"location"`
		Links    map[string]string `json:"links"`
		Summary  string            `json:"summary"`
	} `json:"person"`
	Experiences []struct {
		Title       string   `json:"title"`
		Company     string   `json:"company"`
		Location    string   `json:"location"`
		StartDate   string   `json:"start_date"`
		EndDate     string   `json:"end_date"`
		Description string   `json:"description"`
		Highlights  []string `json:"highlights"`
		Skills      []string `json:"skills"`
		Domain      string   `json:"domain,omitempty"`
		TeamSize    *int     `json:"team_size,omitempty"`
		BudgetUSD   *int     `json:"budget_usd,omitempty"`
		IsVolunteer bool     `json:"is_volunteer,omitempty"`
		SubProjects []struct {
			Name        string   `json:"name"`
			Description string   `json:"description"`
			Tech        []string `json:"tech"`
			Highlights  []string `json:"highlights"`
		} `json:"sub_projects,omitempty"`
	} `json:"experiences"`
	Educations []struct {
		School     string   `json:"school"`
		Degree     string   `json:"degree"`
		Field      string   `json:"field"`
		StartDate  string   `json:"start_date"`
		EndDate    string   `json:"end_date"`
		GPA        string   `json:"gpa"`
		Highlights []string `json:"highlights"`
	} `json:"educations"`
	Skills []struct {
		Name       string `json:"name"`
		Category   string `json:"category"`
		Level      string `json:"level"`
		IsImplicit bool   `json:"is_implicit,omitempty"`
		Source     string `json:"source,omitempty"`
	} `json:"skills"`
	Projects []struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		URL         string   `json:"url"`
		Tech        []string `json:"tech"`
		Highlights  []string `json:"highlights"`
	} `json:"projects"`
	Achievements []struct {
		Text          string   `json:"text"`
		Metric        string   `json:"metric"`
		Value         string   `json:"value"`
		Context       string   `json:"context"`
		MetricNumeric *float64 `json:"metric_numeric,omitempty"`
		MetricUnit    string   `json:"metric_unit,omitempty"`
	} `json:"achievements"`
	Certifications []struct {
		Name   string `json:"name"`
		Issuer string `json:"issuer"`
		Year   string `json:"year"`
	} `json:"certifications"`
	Domains       []string `json:"domains,omitempty"`
	Methodologies []struct {
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
	} `json:"methodologies,omitempty"`
}

type enrichmentResult struct {
	ImplicitSkills []struct {
		Name     string `json:"name"`
		Category string `json:"category"`
		Level    string `json:"level"`
		Source   string `json:"source"` // which experience/achievement it was inferred from
	} `json:"implicit_skills"`
	SubProjects []struct {
		ParentExperience string   `json:"parent_experience"` // company or title to match
		Name             string   `json:"name"`
		Description      string   `json:"description"`
		Tech             []string `json:"tech"`
		Highlights       []string `json:"highlights"`
	} `json:"sub_projects"`
	SkillAdjacencies []struct {
		From string `json:"from"`
		To   string `json:"to"`
	} `json:"skill_adjacencies"`
	CareerTrajectory []struct {
		From string `json:"from"` // earlier role (company or title)
		To   string `json:"to"`   // later role (company or title)
	} `json:"career_trajectory"`
	Methodologies []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"methodologies"`
	Domains []string `json:"domains"`
}

const masterResumeParsePrompt = `You are an expert resume parser. Parse the following resume into structured JSON.
Extract EVERYTHING — every role, skill, project, achievement, certification, and education entry.

For each experience:
- List ALL skills/technologies used in that role in the "skills" field
- Identify the domain (e.g., "Event Production", "Digital Marketing", "Software Engineering", "Media")
- If team_size or budget are mentioned, extract them
- If the role was volunteer/unpaid, set is_volunteer to true
- CRITICALLY: If an experience contains sub-events, sub-products, or sub-projects (e.g., a festival company that ran multiple events), extract them in "sub_projects"

For achievements:
- Extract the metric and numeric value when available
- Set metric_numeric as a float (e.g., 16000 for "16K tickets") and metric_unit (e.g., "tickets", "percent", "USD")

For skills:
- Mark explicitly listed skills with is_implicit: false, source: "resume"
- If you can clearly infer skills from context (e.g., "sold 16K tickets with zero budget" implies Guerrilla Marketing), add them with is_implicit: true, source: "inferred"

Also extract:
- "domains": array of professional domains the person operates in (e.g., ["Event Production", "Digital Marketing", "Media"])
- "methodologies": array of approaches/frameworks the person uses (e.g., [{"name": "Zero-Budget Growth", "description": "..."}])

Return a JSON object with this exact structure:
{
  "person": {
    "name": "...",
    "email": "...",
    "phone": "...",
    "location": "...",
    "links": {"linkedin": "url", "github": "url", ...},
    "summary": "professional summary if present"
  },
  "experiences": [
    {
      "title": "...",
      "company": "...",
      "location": "...",
      "start_date": "YYYY-MM or YYYY",
      "end_date": "YYYY-MM or Present",
      "description": "brief role description",
      "highlights": ["bullet point 1", "bullet point 2"],
      "skills": ["Go", "PostgreSQL", "Docker"],
      "domain": "Software Engineering",
      "team_size": null,
      "budget_usd": null,
      "is_volunteer": false,
      "sub_projects": [
        {"name": "...", "description": "...", "tech": [], "highlights": []}
      ]
    }
  ],
  "educations": [
    {"school": "...", "degree": "...", "field": "...", "start_date": "...", "end_date": "...", "gpa": "...", "highlights": []}
  ],
  "skills": [
    {"name": "Go", "category": "programming_language", "level": "expert", "is_implicit": false, "source": "resume"}
  ],
  "projects": [
    {"name": "...", "description": "...", "url": "...", "tech": ["Go", "Redis"], "highlights": ["..."]}
  ],
  "achievements": [
    {"text": "Sold 16K tickets with zero marketing budget", "metric": "tickets sold", "value": "16000", "context": "Festival Empire", "metric_numeric": 16000, "metric_unit": "tickets"}
  ],
  "certifications": [
    {"name": "...", "issuer": "...", "year": "..."}
  ],
  "domains": ["Event Production", "Digital Marketing"],
  "methodologies": [
    {"name": "Zero-Budget Growth", "description": "Driving massive outcomes without paid advertising through viral mechanics and psychological triggers"}
  ]
}

Skill categories: programming_language, framework, database, cloud, devops, tool, methodology, soft_skill, other.
Skill levels: expert, advanced, intermediate, beginner (infer from context — primary stack = expert, mentioned once = intermediate).

IMPORTANT: Do NOT skip any section. The "educations" array MUST be populated if the resume contains education info (look at the bottom of the resume). Same for certifications.
Be aggressive about extracting sub_projects from experiences — if an experience mentions multiple distinct initiatives, events, products, or campaigns, each one is a sub_project.
Be aggressive about inferring implicit skills — read between the lines of achievements and responsibilities.

RESUME:
%s

Return ONLY the JSON object, no markdown, no explanation.`

const enrichmentPrompt = `You are an expert career analyst. Given this parsed resume data and the original resume text, enrich it with deeper insights.

PARSED DATA:
%s

ORIGINAL RESUME:
%s

Analyze and return a JSON object with:

1. "implicit_skills": Skills that can be inferred but were not explicitly stated. For each:
   - "name": skill name
   - "category": one of programming_language, framework, database, cloud, devops, tool, methodology, soft_skill, other
   - "level": expert/advanced/intermediate/beginner
   - "source": which experience or achievement this was inferred from

2. "sub_projects": Hidden sub-projects within experiences. For each:
   - "parent_experience": the company name to match to parent experience
   - "name": project name
   - "description": what it was
   - "tech": technologies used
   - "highlights": key results

3. "skill_adjacencies": Pairs of skills where knowing one implies the other. For each:
   - "from": skill name
   - "to": implied skill name

4. "career_trajectory": Pairs showing career evolution (same domain, higher role). For each:
   - "from": earlier company name
   - "to": later company name

5. "methodologies": Unique approaches or frameworks this person developed or heavily used. For each:
   - "name": methodology name
   - "description": brief description

6. "domains": Professional domains (e.g., "Event Production", "Digital Marketing", "Media", "Software Engineering")

Focus on HIGH-VALUE enrichments that would improve ATS matching and showcase hidden strengths.
Do NOT duplicate items already in the parsed data.

Return ONLY the JSON object, no markdown, no explanation.`

// BuildMasterResume parses resume text into SQL tables + AGE graph + MemDB vectors.
func BuildMasterResume(ctx context.Context, resumeText string) (*MasterResumeBuildResult, error) { //nolint:funlen
	db := GetResumeDB()
	if db == nil {
		return nil, errors.New("resume database not configured (set DATABASE_URL)")
	}

	// 1. Parse resume via LLM (call #1)
	resumeTrunc := engine.TruncateRunes(resumeText, 12000, "")
	prompt := fmt.Sprintf(masterResumeParsePrompt, resumeTrunc)

	raw, err := engine.CallLLM(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("master_resume_build LLM: %w", err)
	}

	raw = StripMarkdownFences(raw)

	var parsed parsedResume
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("master_resume_build parse: %w (raw: %s)", err, engine.TruncateRunes(raw, 200, "..."))
	}

	// 2. Enrichment pass (LLM call #2)
	parsedJSON, _ := json.Marshal(parsed)
	enrichPrompt := fmt.Sprintf(enrichmentPrompt,
		engine.TruncateRunes(string(parsedJSON), 8000, ""),
		engine.TruncateRunes(resumeText, 6000, ""),
	)

	enrichRaw, err := engine.CallLLM(ctx, enrichPrompt)
	if err != nil {
		slog.Warn("enrichment LLM call failed, continuing without enrichment", slog.Any("error", err))
	}

	var enrichment enrichmentResult
	if enrichRaw != "" {
		enrichRaw = StripMarkdownFences(enrichRaw)
		if err := json.Unmarshal([]byte(enrichRaw), &enrichment); err != nil {
			slog.Warn("enrichment parse failed, continuing without enrichment", slog.Any("error", err))
		}
	}

	// 3. Clear existing data (single-user, rebuild from scratch)
	if err := db.ClearAllPersons(ctx); err != nil {
		slog.Debug("clear persons failed", slog.Any("error", err))
	}
	if err := db.ClearGraph(ctx); err != nil {
		slog.Debug("clear graph failed", slog.Any("error", err))
	}

	// Clear MemDB vectors
	mdb := GetMemDB()
	if mdb != nil {
		if err := mdb.ClearAllBySearch(ctx); err != nil {
			slog.Debug("memdb clear failed", slog.Any("error", err))
		}
	}

	// 4. Insert person
	personID, err := db.InsertPerson(ctx, PersonRecord{
		Name:     parsed.Person.Name,
		Email:    parsed.Person.Email,
		Phone:    parsed.Person.Phone,
		Location: parsed.Person.Location,
		Links:    parsed.Person.Links,
		Summary:  parsed.Person.Summary,
	})
	if err != nil {
		return nil, fmt.Errorf("insert person: %w", err)
	}

	result := &MasterResumeBuildResult{PersonID: personID}
	var vectorTexts []vectorEntry

	// Track skill name → skill ID for graph edges
	skillIDs := make(map[string]int)

	// Track experience company → expID for enrichment linking
	expByCompany := make(map[string]int)

	// 5. Insert standalone skills (both explicit and implicit from parse)
	for _, s := range parsed.Skills {
		source := s.Source
		if source == "" {
			source = "resume"
		}
		sid, err := db.InsertSkillExtended(ctx, personID, SkillRecord{
			Name:       s.Name,
			Category:   s.Category,
			Level:      s.Level,
			IsImplicit: s.IsImplicit,
			Source:     source,
		})
		if err != nil {
			slog.Debug("insert skill failed", slog.String("name", s.Name), slog.Any("error", err))
			continue
		}
		skillIDs[strings.ToLower(s.Name)] = sid
		result.Skills++
		if s.IsImplicit {
			result.ImplicitSkills++
		}
	}

	// 6. Insert experiences + graph nodes/edges
	for _, exp := range parsed.Experiences {
		expID, err := db.InsertExperience(ctx, personID, ExperienceRecord{
			Title:       exp.Title,
			Company:     exp.Company,
			Location:    exp.Location,
			StartDate:   exp.StartDate,
			EndDate:     exp.EndDate,
			Description: exp.Description,
			Highlights:  exp.Highlights,
		})
		if err != nil {
			slog.Debug("insert experience failed", slog.String("title", exp.Title), slog.Any("error", err))
			continue
		}
		result.Experiences++
		expByCompany[strings.ToLower(exp.Company)] = expID

		// Update extended metadata
		if exp.Domain != "" || exp.TeamSize != nil || exp.BudgetUSD != nil || exp.IsVolunteer {
			if err := db.UpdateExperienceMeta(ctx, expID, exp.TeamSize, exp.BudgetUSD, exp.Domain, exp.IsVolunteer); err != nil {
				slog.Debug("update experience meta failed", slog.Int("exp_id", expID), slog.Any("error", err))
			}
		}

		// Graph: Exp node
		if err := db.UpsertGraphNode(ctx, "Exp", expID, map[string]string{
			"title":   exp.Title,
			"company": exp.Company,
		}); err != nil {
			slog.Debug("graph node upsert failed", slog.Any("error", err))
		}

		// Graph: skill edges
		for _, skillName := range exp.Skills {
			sid := ensureSkill(ctx, db, personID, skillName, "other", "intermediate", false, "resume", skillIDs, result)
			if sid > 0 {
				if err := db.UpsertGraphNode(ctx, "Skill", sid, map[string]string{"name": skillName}); err != nil {
					slog.Debug("graph node upsert failed", slog.Any("error", err))
				}
				if err := db.UpsertGraphEdge(ctx, "Exp", expID, "USED_SKILL", "Skill", sid); err != nil {
					slog.Debug("graph edge upsert failed", slog.Any("error", err))
				}
			}
		}

		// Graph: domain edge
		if exp.Domain != "" {
			domID, err := db.InsertDomain(ctx, personID, exp.Domain)
			if err != nil {
				slog.Warn("insert exp domain failed", slog.String("domain", exp.Domain), slog.Any("error", err))
			} else {
				if err := db.UpsertGraphNode(ctx, "Domain", domID, map[string]string{"name": exp.Domain}); err != nil {
					slog.Debug("graph node upsert failed", slog.Any("error", err))
				}
				if err := db.UpsertGraphEdge(ctx, "Exp", expID, "IN_DOMAIN", "Domain", domID); err != nil {
					slog.Debug("graph edge upsert failed", slog.Any("error", err))
				}
			}
		}

		// Insert sub-projects from parse
		for _, sp := range exp.SubProjects {
			spID, err := db.InsertProjectWithParent(ctx, personID, &expID, ProjectRecord{
				Name:        sp.Name,
				Description: sp.Description,
				Tech:        sp.Tech,
				Highlights:  sp.Highlights,
			})
			if err != nil {
				slog.Debug("insert sub-project failed", slog.String("name", sp.Name), slog.Any("error", err))
				continue
			}
			result.Projects++
			result.SubProjects++

			if err := db.UpsertGraphNode(ctx, "Proj", spID, map[string]string{"name": sp.Name}); err != nil {
				slog.Debug("graph node upsert failed", slog.Any("error", err))
			}
			if err := db.UpsertGraphEdge(ctx, "Proj", spID, "PART_OF", "Exp", expID); err != nil {
				slog.Debug("graph edge upsert failed", slog.Any("error", err))
			}

			for _, techName := range sp.Tech {
				sid := ensureSkill(ctx, db, personID, techName, "other", "intermediate", false, "resume", skillIDs, result)
				if sid > 0 {
					if err := db.UpsertGraphNode(ctx, "Skill", sid, map[string]string{"name": techName}); err != nil {
						slog.Debug("graph node upsert failed", slog.Any("error", err))
					}
					if err := db.UpsertGraphEdge(ctx, "Proj", spID, "USED_SKILL", "Skill", sid); err != nil {
						slog.Debug("graph edge upsert failed", slog.Any("error", err))
					}
				}
			}

			text := formatProjectText(sp.Name, sp.Description, sp.Tech, sp.Highlights)
			vectorTexts = append(vectorTexts, vectorEntry{
				content: text,
				info:    map[string]any{"type": "project", "id": float64(spID)},
			})
		}

		// Vector: experience text (with domain context)
		text := formatExperienceTextExtended(exp.Title, exp.Company, exp.StartDate, exp.EndDate, exp.Description, exp.Highlights, exp.Domain)
		vectorTexts = append(vectorTexts, vectorEntry{
			content: text,
			info:    map[string]any{"type": "experience", "id": float64(expID)},
		})
	}

	// 7. Insert standalone projects + graph
	for _, proj := range parsed.Projects {
		projID, err := db.InsertProject(ctx, personID, ProjectRecord{
			Name:        proj.Name,
			Description: proj.Description,
			URL:         proj.URL,
			Tech:        proj.Tech,
			Highlights:  proj.Highlights,
		})
		if err != nil {
			slog.Debug("insert project failed", slog.String("name", proj.Name), slog.Any("error", err))
			continue
		}
		result.Projects++

		if err := db.UpsertGraphNode(ctx, "Proj", projID, map[string]string{"name": proj.Name}); err != nil {
			slog.Debug("graph node upsert failed", slog.Any("error", err))
		}

		for _, techName := range proj.Tech {
			sid := ensureSkill(ctx, db, personID, techName, "other", "intermediate", false, "resume", skillIDs, result)
			if sid > 0 {
				if err := db.UpsertGraphNode(ctx, "Skill", sid, map[string]string{"name": techName}); err != nil {
					slog.Debug("graph node upsert failed", slog.Any("error", err))
				}
				if err := db.UpsertGraphEdge(ctx, "Proj", projID, "USED_SKILL", "Skill", sid); err != nil {
					slog.Debug("graph edge upsert failed", slog.Any("error", err))
				}
			}
		}

		text := formatProjectText(proj.Name, proj.Description, proj.Tech, proj.Highlights)
		vectorTexts = append(vectorTexts, vectorEntry{
			content: text,
			info:    map[string]any{"type": "project", "id": float64(projID)},
		})
	}

	// 8. Insert achievements + graph
	for i, achv := range parsed.Achievements {
		achvID, err := db.InsertAchievementExtended(ctx, personID, AchievementRecord{
			Text:          achv.Text,
			Metric:        achv.Metric,
			Value:         achv.Value,
			Context:       achv.Context,
			MetricNumeric: achv.MetricNumeric,
			MetricUnit:    achv.MetricUnit,
		})
		if err != nil {
			slog.Debug("insert achievement failed", slog.Int("index", i), slog.Any("error", err))
			continue
		}
		result.Achievements++

		if err := db.UpsertGraphNode(ctx, "Achv", achvID, map[string]string{"text": achv.Text}); err != nil {
			slog.Debug("graph node upsert failed", slog.Any("error", err))
		}

		// Link to parent experience/project by context match
		if achv.Context != "" {
			linkAchievementToParent(ctx, db, achv.Context, achvID, personID)
		}

		vectorTexts = append(vectorTexts, vectorEntry{
			content: achv.Text,
			info:    map[string]any{"type": "achievement", "id": float64(achvID)},
		})
	}

	// 9. Insert educations
	for _, edu := range parsed.Educations {
		_, err := db.InsertEducation(ctx, personID, EducationRecord{
			School:     edu.School,
			Degree:     edu.Degree,
			Field:      edu.Field,
			StartDate:  edu.StartDate,
			EndDate:    edu.EndDate,
			GPA:        edu.GPA,
			Highlights: edu.Highlights,
		})
		if err != nil {
			slog.Debug("insert education failed", slog.String("school", edu.School), slog.Any("error", err))
			continue
		}
		result.Educations++
	}

	// 10. Insert certifications
	for _, cert := range parsed.Certifications {
		_, err := db.InsertCertification(ctx, personID, CertificationRecord{
			Name:   cert.Name,
			Issuer: cert.Issuer,
			Year:   cert.Year,
		})
		if err != nil {
			slog.Debug("insert certification failed", slog.String("name", cert.Name), slog.Any("error", err))
			continue
		}
		result.Certifications++
	}

	// 11. Insert domains (from parse + enrichment)
	allDomains := make(map[string]bool)
	for _, d := range parsed.Domains {
		allDomains[d] = true
	}
	for _, d := range enrichment.Domains {
		allDomains[d] = true
	}
	for d := range allDomains {
		domID, err := db.InsertDomain(ctx, personID, d)
		if err != nil {
			slog.Warn("insert domain failed", slog.String("name", d), slog.Any("error", err))
			continue
		}
		if err := db.UpsertGraphNode(ctx, "Domain", domID, map[string]string{"name": d}); err != nil {
			slog.Debug("graph node upsert failed", slog.Any("error", err))
		}
		result.Domains++
	}

	// 12. Insert methodologies (from parse + enrichment)
	allMethods := make(map[string]string) // name → description
	for _, m := range parsed.Methodologies {
		allMethods[m.Name] = m.Description
	}
	for _, m := range enrichment.Methodologies {
		if _, exists := allMethods[m.Name]; !exists {
			allMethods[m.Name] = m.Description
		}
	}
	for name, desc := range allMethods {
		methID, err := db.InsertMethodology(ctx, personID, name, desc)
		if err != nil {
			slog.Warn("insert methodology failed", slog.String("name", name), slog.Any("error", err))
			continue
		}
		if err := db.UpsertGraphNode(ctx, "Method", methID, map[string]string{"name": name}); err != nil {
			slog.Debug("graph node upsert failed", slog.Any("error", err))
		}
		result.Methodologies++
	}

	// 13. Apply enrichment: implicit skills
	for _, is := range enrichment.ImplicitSkills {
		if _, exists := skillIDs[strings.ToLower(is.Name)]; exists {
			continue // already have this skill
		}
		sid := ensureSkill(ctx, db, personID, is.Name, is.Category, is.Level, true, "inferred", skillIDs, result)
		if sid > 0 {
			result.ImplicitSkills++
			if err := db.UpsertGraphNode(ctx, "Skill", sid, map[string]string{"name": is.Name}); err != nil {
				slog.Debug("graph node upsert failed", slog.Any("error", err))
			}

			// DERIVED_SKILL: link from achievement context if possible
			if is.Source != "" {
				linkImplicitSkillToSource(ctx, db, is.Source, sid, personID)
			}
		}
	}

	// 14. Apply enrichment: sub-projects
	for _, sp := range enrichment.SubProjects {
		parentExpID := findExperienceByHint(expByCompany, sp.ParentExperience)
		var parentPtr *int
		if parentExpID > 0 {
			parentPtr = &parentExpID
		}

		spID, err := db.InsertProjectWithParent(ctx, personID, parentPtr, ProjectRecord{
			Name:        sp.Name,
			Description: sp.Description,
			Tech:        sp.Tech,
			Highlights:  sp.Highlights,
		})
		if err != nil {
			continue
		}
		result.Projects++
		result.SubProjects++

		if err := db.UpsertGraphNode(ctx, "Proj", spID, map[string]string{"name": sp.Name}); err != nil {
			slog.Debug("graph node upsert failed", slog.Any("error", err))
		}
		if parentExpID > 0 {
			if err := db.UpsertGraphEdge(ctx, "Proj", spID, "PART_OF", "Exp", parentExpID); err != nil {
				slog.Debug("graph edge upsert failed", slog.Any("error", err))
			}
		}

		for _, techName := range sp.Tech {
			sid := ensureSkill(ctx, db, personID, techName, "other", "intermediate", false, "resume", skillIDs, result)
			if sid > 0 {
				if err := db.UpsertGraphNode(ctx, "Skill", sid, map[string]string{"name": techName}); err != nil {
					slog.Debug("graph node upsert failed", slog.Any("error", err))
				}
				if err := db.UpsertGraphEdge(ctx, "Proj", spID, "USED_SKILL", "Skill", sid); err != nil {
					slog.Debug("graph edge upsert failed", slog.Any("error", err))
				}
			}
		}

		text := formatProjectText(sp.Name, sp.Description, sp.Tech, sp.Highlights)
		vectorTexts = append(vectorTexts, vectorEntry{
			content: text,
			info:    map[string]any{"type": "project", "id": float64(spID)},
		})
	}

	// 15. Apply enrichment: skill adjacencies (IMPLIES_SKILL edges)
	for _, adj := range enrichment.SkillAdjacencies {
		fromID, ok := skillIDs[strings.ToLower(adj.From)]
		if !ok {
			continue
		}
		toID := ensureSkill(ctx, db, personID, adj.To, "other", "intermediate", true, "inferred", skillIDs, result)
		if toID > 0 {
			if err := db.UpsertGraphNode(ctx, "Skill", toID, map[string]string{"name": adj.To}); err != nil {
				slog.Debug("graph node upsert failed", slog.Any("error", err))
			}
			if err := db.UpsertGraphEdge(ctx, "Skill", fromID, "IMPLIES_SKILL", "Skill", toID); err != nil {
				slog.Debug("graph edge upsert failed", slog.Any("error", err))
			}
		}
	}

	// 16. Apply enrichment: career trajectory (EVOLVED_TO edges)
	for _, ct := range enrichment.CareerTrajectory {
		fromExpID := findExperienceByHint(expByCompany, ct.From)
		toExpID := findExperienceByHint(expByCompany, ct.To)
		if fromExpID > 0 && toExpID > 0 {
			if err := db.UpsertGraphEdge(ctx, "Exp", fromExpID, "EVOLVED_TO", "Exp", toExpID); err != nil {
				slog.Debug("graph edge upsert failed", slog.Any("error", err))
			}
		}
	}

	// 17. Link methodologies to experiences via USED_METHOD
	exps, _ := db.GetAllExperiences(ctx, personID)
	methods, _ := db.GetAllMethodologies(ctx, personID)
	for _, exp := range exps {
		expText := strings.ToLower(exp.Description + " " + strings.Join(exp.Highlights, " "))
		for _, m := range methods {
			if strings.Contains(expText, strings.ToLower(m.Name)) {
				if err := db.UpsertGraphEdge(ctx, "Exp", exp.ID, "USED_METHOD", "Method", m.ID); err != nil {
					slog.Debug("graph edge upsert failed", slog.Any("error", err))
				}
			}
		}
	}

	// 18. Graph counts
	if nodes, err := db.CountGraphNodes(ctx); err == nil {
		result.GraphNodes = nodes
	}
	if edges, err := db.CountGraphEdges(ctx); err == nil {
		result.GraphEdges = edges
	}

	// 19. Sync to MemDB
	if mdb != nil {
		for _, ve := range vectorTexts {
			if _, err := mdb.Add(ctx, ve.content, ve.info); err != nil {
				slog.Debug("memdb add failed", slog.Any("error", err))
				continue
			}
			result.VectorsStored++
		}
	}

	// 20. Mark person as enriched
	if err := db.MarkPersonEnriched(ctx, personID); err != nil {
		slog.Debug("mark person enriched failed", slog.Int("person_id", personID), slog.Any("error", err))
	}

	result.Summary = fmt.Sprintf("Master resume built for %s: %d experiences, %d skills (%d implicit), %d projects (%d sub-projects), %d achievements, %d educations, %d certifications, %d domains, %d methodologies. Graph: %d nodes, %d edges. Vectors: %d stored.",
		parsed.Person.Name,
		result.Experiences, result.Skills, result.ImplicitSkills,
		result.Projects, result.SubProjects,
		result.Achievements, result.Educations, result.Certifications,
		result.Domains, result.Methodologies,
		result.GraphNodes, result.GraphEdges, result.VectorsStored,
	)

	slog.Info("master resume built",
		slog.Int("person_id", personID),
		slog.Int("experiences", result.Experiences),
		slog.Int("skills", result.Skills),
		slog.Int("implicit_skills", result.ImplicitSkills),
		slog.Int("sub_projects", result.SubProjects),
		slog.Int("domains", result.Domains),
		slog.Int("methodologies", result.Methodologies),
		slog.Int("graph_nodes", result.GraphNodes),
		slog.Int("vectors", result.VectorsStored),
	)

	return result, nil
}

// ensureSkill inserts or retrieves a skill, updating the tracking map and result counter.
func ensureSkill(ctx context.Context, db *ResumeDB, personID int, name, category, level string, isImplicit bool, source string, skillIDs map[string]int, result *MasterResumeBuildResult) int {
	key := strings.ToLower(name)
	if sid, ok := skillIDs[key]; ok {
		return sid
	}
	sid, err := db.InsertSkillExtended(ctx, personID, SkillRecord{
		Name:       name,
		Category:   category,
		Level:      level,
		IsImplicit: isImplicit,
		Source:     source,
	})
	if err != nil {
		return 0
	}
	skillIDs[key] = sid
	result.Skills++
	return sid
}

// findExperienceByHint looks up an experience ID by matching company name (case-insensitive).
func findExperienceByHint(expByCompany map[string]int, hint string) int {
	hint = strings.ToLower(hint)
	if id, ok := expByCompany[hint]; ok {
		return id
	}
	// Partial match
	for company, id := range expByCompany {
		if strings.Contains(company, hint) || strings.Contains(hint, company) {
			return id
		}
	}
	return 0
}

// linkImplicitSkillToSource creates a DERIVED_SKILL edge from the matching achievement to the skill.
func linkImplicitSkillToSource(ctx context.Context, db *ResumeDB, sourceHint string, skillID int, personID int) {
	hint := strings.ToLower(sourceHint)
	achvs, _ := db.GetAllAchievements(ctx, personID)
	for _, a := range achvs {
		if strings.Contains(strings.ToLower(a.Text), hint) || strings.Contains(strings.ToLower(a.Context), hint) {
			if err := db.UpsertGraphEdge(ctx, "Achv", a.ID, "DERIVED_SKILL", "Skill", skillID); err != nil {
				slog.Debug("graph edge upsert failed", slog.Any("error", err))
			}
			return
		}
	}
}

type vectorEntry struct {
	content string
	info    map[string]any
}

func formatExperienceTextExtended(title, company, startDate, endDate, description string, highlights []string, domain string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s at %s (%s–%s)", title, company, startDate, endDate)
	if domain != "" {
		fmt.Fprintf(&b, " [%s]", domain)
	}
	if description != "" {
		fmt.Fprintf(&b, ": %s", description)
	}
	for _, h := range highlights {
		fmt.Fprintf(&b, " | %s", h)
	}
	return b.String()
}

func formatProjectText(name, description string, tech []string, highlights []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Project %s", name)
	if description != "" {
		fmt.Fprintf(&b, ": %s", description)
	}
	if len(tech) > 0 {
		fmt.Fprintf(&b, " [%s]", strings.Join(tech, ", "))
	}
	for _, h := range highlights {
		fmt.Fprintf(&b, " | %s", h)
	}
	return b.String()
}

// linkAchievementToParent creates a PRODUCED edge from the matching experience/project to the achievement.
func linkAchievementToParent(ctx context.Context, db *ResumeDB, contextHint string, achvID int, personID int) {
	hint := strings.ToLower(contextHint)

	// Try experiences
	exps, _ := db.GetAllExperiences(ctx, personID)
	for _, exp := range exps {
		if strings.Contains(hint, strings.ToLower(exp.Company)) || strings.Contains(hint, strings.ToLower(exp.Title)) {
			if err := db.UpsertGraphEdge(ctx, "Exp", exp.ID, "PRODUCED", "Achv", achvID); err != nil {
				slog.Debug("graph edge upsert failed", slog.Any("error", err))
			}
			return
		}
	}

	// Try projects
	projs, _ := db.GetAllProjects(ctx, personID)
	for _, proj := range projs {
		if strings.Contains(hint, strings.ToLower(proj.Name)) {
			if err := db.UpsertGraphEdge(ctx, "Proj", proj.ID, "PRODUCED", "Achv", achvID); err != nil {
				slog.Debug("graph edge upsert failed", slog.Any("error", err))
			}
			return
		}
	}
}

// StripMarkdownFences removes ```json and ``` wrappers from LLM output.
func StripMarkdownFences(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	return strings.TrimSpace(raw)
}
