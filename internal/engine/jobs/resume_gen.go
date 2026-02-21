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

// ResumeGenerateResult is the structured output of resume_generate.
type ResumeGenerateResult struct {
	Resume          string   `json:"resume"`
	ATSScore        int      `json:"ats_score"`
	MatchedKeywords []string `json:"matched_keywords"`
	AddedKeywords   []string `json:"added_keywords"`
	MissingKeywords []string `json:"missing_keywords"`
	SelectedItems   struct {
		Experiences  int `json:"experiences"`
		Projects     int `json:"projects"`
		Achievements int `json:"achievements"`
	} `json:"selected_items"`
	Summary string `json:"summary"`
}

type jdRequirements struct {
	RequiredSkills  []string `json:"required_skills"`
	NiceToHave      []string `json:"nice_to_have"`
	KeyRequirements []string `json:"key_requirements"`
	RoleTitle       string   `json:"role_title"`
	Seniority       string   `json:"seniority"`
}

const jdExtractPrompt = `Analyze the following job description and extract requirements.

Return a JSON object with this exact structure:
{
  "required_skills": ["skill1", "skill2"],
  "nice_to_have": ["skill1", "skill2"],
  "key_requirements": ["requirement1", "requirement2"],
  "role_title": "normalized role title",
  "seniority": "junior/mid/senior/lead/staff/principal"
}

JOB DESCRIPTION:
%s

Return ONLY the JSON object.`

const resumeAssemblePrompt = `You are an expert ATS resume writer. Create an ATS-optimized resume tailored to the target job.

TARGET ROLE: %s (%s level)
REQUIRED SKILLS: %s
NICE-TO-HAVE SKILLS: %s

CANDIDATE DATA:
%s

%sGUIDELINES:
- Use clean, ATS-friendly formatting (no tables, no columns, no graphics)
- Start with a strong professional summary targeting this specific role
- List experiences in reverse chronological order
- Use STAR format for bullet points (Situation, Task, Action, Result)
- Naturally incorporate ALL required keywords — do NOT force them
- Prioritize the most relevant experiences and projects for this role
- Quantify achievements with numbers wherever possible
- Include a skills section grouped by category
- Keep it to 1-2 pages (for senior roles, 2 pages is fine)

FORMAT: %s

Return a JSON object with this exact structure:
{
  "resume": "<the complete tailored resume text>",
  "ats_score": <estimated ATS match score 0-100>,
  "matched_keywords": [<keywords from JD that are in the resume>],
  "added_keywords": [<keywords you added to improve match>],
  "missing_keywords": [<JD keywords that could not be naturally incorporated>]
}

Return ONLY the JSON object, no markdown, no explanation.`

// GenerateResume queries the master resume graph + vectors against a JD and assembles an ATS-optimized resume.
func GenerateResume(ctx context.Context, jobDescription, company, format string) (*ResumeGenerateResult, error) {
	db := GetResumeDB()
	if db == nil {
		return nil, errors.New("resume database not configured (set DATABASE_URL)")
	}

	personID := db.GetLatestPersonID(ctx)
	if personID == 0 {
		return nil, errors.New("no master resume found — run master_resume_build first")
	}

	if format == "" {
		format = "text"
	}

	// 1. Extract JD requirements (LLM call #1)
	jdTrunc := engine.TruncateRunes(jobDescription, 3000, "")
	jdPrompt := fmt.Sprintf(jdExtractPrompt, jdTrunc)
	jdRaw, err := engine.CallLLM(ctx, jdPrompt)
	if err != nil {
		return nil, fmt.Errorf("resume_generate extract JD: %w", err)
	}

	jdRaw = stripMarkdownFences(jdRaw)

	var jd jdRequirements
	if err := json.Unmarshal([]byte(jdRaw), &jd); err != nil {
		return nil, fmt.Errorf("resume_generate parse JD: %w (raw: %s)", err, engine.TruncateRunes(jdRaw, 200, "..."))
	}

	// 2. Query graph for matching experiences & projects by skill
	expIDSet := make(map[int]bool)
	projIDSet := make(map[int]bool)
	achvIDSet := make(map[int]bool)

	allSkills := make([]string, 0, len(jd.RequiredSkills)+len(jd.NiceToHave))
	allSkills = append(allSkills, jd.RequiredSkills...)
	allSkills = append(allSkills, jd.NiceToHave...)
	for _, skill := range allSkills {
		// Experience by direct skill
		expIDs, err := db.QueryExperienceIDsBySkill(ctx, skill)
		if err != nil {
			slog.Debug("graph query exp by skill failed", slog.String("skill", skill), slog.Any("error", err))
		}
		for _, id := range expIDs {
			expIDSet[id] = true
			// Achievements linked to this experience
			achvIDs, _ := db.QueryAchievementIDsByExperience(ctx, id)
			for _, aid := range achvIDs {
				achvIDSet[aid] = true
			}
			// Sub-projects linked to this experience via PART_OF
			subProjIDs, _ := db.QuerySubProjectIDs(ctx, id)
			for _, spid := range subProjIDs {
				projIDSet[spid] = true
			}
		}

		// Projects by skill
		projIDs, err := db.QueryProjectIDsBySkill(ctx, skill)
		if err != nil {
			slog.Debug("graph query proj by skill failed", slog.String("skill", skill), slog.Any("error", err))
		}
		for _, id := range projIDs {
			projIDSet[id] = true
		}

		// Traverse IMPLIES_SKILL: find experiences/projects via adjacent skills
		skillID := db.QuerySkillIDByName(ctx, personID, skill)
		if skillID > 0 {
			impliedIDs, _ := db.QueryImpliedSkillIDs(ctx, skillID)
			for _, impliedID := range impliedIDs {
				// Experiences using the implied skill
				iExpIDs, _ := db.QueryExperienceIDsBySkill(ctx, skill)
				_ = iExpIDs // implied skills don't have name here, query by ID not possible directly
				// Use a Cypher query to find experiences via implied skill ID
				iExpIDs2, _ := queryExperienceIDsBySkillID(ctx, db, impliedID)
				for _, id := range iExpIDs2 {
					expIDSet[id] = true
				}
			}
		}
	}

	// 3. Vector search for semantic matches (MemDB)
	mdb := GetMemDB()
	if mdb != nil {
		results, err := mdb.Search(ctx, jdTrunc, 15, 0.6)
		if err != nil {
			slog.Debug("memdb search failed", slog.Any("error", err))
		} else {
			for _, r := range results {
				itemType, _ := r.Info["type"].(string)
				itemID, _ := r.Info["id"].(float64)
				id := int(itemID)
				if id == 0 {
					continue
				}
				switch itemType {
				case "experience":
					expIDSet[id] = true
				case "project":
					projIDSet[id] = true
				case "achievement":
					achvIDSet[id] = true
				}
			}
		}
	}

	// 4. Fetch full records from SQL
	expIDs := intSetToSlice(expIDSet)
	projIDs := intSetToSlice(projIDSet)
	achvIDs := intSetToSlice(achvIDSet)

	experiences, _ := db.GetExperiencesByIDs(ctx, expIDs)
	projects, _ := db.GetProjectsByIDs(ctx, projIDs)
	achievements, _ := db.GetAchievementsByIDs(ctx, achvIDs)

	// If graph/vector returned nothing, fall back to all data
	if len(experiences) == 0 {
		experiences, _ = db.GetAllExperiences(ctx, personID)
	}
	if len(projects) == 0 {
		projects, _ = db.GetAllProjects(ctx, personID)
	}
	if len(achievements) == 0 {
		achievements, _ = db.GetAllAchievements(ctx, personID)
	}

	// Always include education, skills, certifications, domains, methodologies
	educations, _ := db.GetAllEducations(ctx, personID)
	skills, _ := db.GetAllSkills(ctx, personID)
	certifications, _ := db.GetAllCertifications(ctx, personID)
	domains, _ := db.GetAllDomains(ctx, personID)
	methodologies, _ := db.GetAllMethodologies(ctx, personID)

	// 5. Format candidate data for LLM
	candidateData := formatCandidateData(experiences, projects, achievements, educations, skills, certifications, domains, methodologies)

	// 6. Optional company enrichment
	companyContext := ""
	if company != "" {
		cr, err := ResearchCompany(ctx, company)
		if err == nil && cr != nil {
			var parts []string
			if len(cr.TechStack) > 0 {
				parts = append(parts, "Tech stack: "+strings.Join(cr.TechStack, ", "))
			}
			if cr.CultureNotes != "" {
				parts = append(parts, "Culture: "+cr.CultureNotes)
			}
			if cr.Industry != "" {
				parts = append(parts, "Industry: "+cr.Industry)
			}
			if len(parts) > 0 {
				companyContext = fmt.Sprintf("COMPANY CONTEXT (%s):\n%s\n\n", company, strings.Join(parts, "\n"))
			}
		}
	}

	// 7. Assemble resume (LLM call #2)
	assemblePrompt := fmt.Sprintf(resumeAssemblePrompt,
		jd.RoleTitle,
		jd.Seniority,
		strings.Join(jd.RequiredSkills, ", "),
		strings.Join(jd.NiceToHave, ", "),
		candidateData,
		companyContext,
		format,
	)

	assembleRaw, err := engine.CallLLM(ctx, assemblePrompt)
	if err != nil {
		return nil, fmt.Errorf("resume_generate assemble: %w", err)
	}

	assembleRaw = stripMarkdownFences(assembleRaw)

	var assembled struct {
		Resume          string   `json:"resume"`
		ATSScore        int      `json:"ats_score"`
		MatchedKeywords []string `json:"matched_keywords"`
		AddedKeywords   []string `json:"added_keywords"`
		MissingKeywords []string `json:"missing_keywords"`
	}
	if err := json.Unmarshal([]byte(assembleRaw), &assembled); err != nil {
		// Fallback: if JSON parse fails, treat the raw output as the resume
		return &ResumeGenerateResult{
			Resume:  assembleRaw,
			Summary: "Resume generated (JSON parse failed, returning raw text)",
		}, nil
	}

	result := &ResumeGenerateResult{
		Resume:          assembled.Resume,
		ATSScore:        assembled.ATSScore,
		MatchedKeywords: assembled.MatchedKeywords,
		AddedKeywords:   assembled.AddedKeywords,
		MissingKeywords: assembled.MissingKeywords,
	}
	result.SelectedItems.Experiences = len(experiences)
	result.SelectedItems.Projects = len(projects)
	result.SelectedItems.Achievements = len(achievements)

	result.Summary = fmt.Sprintf("Generated ATS resume for %s (%s). Used %d experiences, %d projects, %d achievements. ATS score: %d/100. Matched %d/%d keywords.",
		jd.RoleTitle, jd.Seniority,
		len(experiences), len(projects), len(achievements),
		result.ATSScore,
		len(result.MatchedKeywords),
		len(jd.RequiredSkills)+len(jd.NiceToHave),
	)

	return result, nil
}

// queryExperienceIDsBySkillID finds experience IDs linked to a skill by its ID.
func queryExperienceIDsBySkillID(ctx context.Context, db *ResumeDB, skillID int) ([]int, error) {
	conn, err := db.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, ageSetup); err != nil {
		return nil, err
	}

	cypher := fmt.Sprintf(`
		SELECT * FROM ag_catalog.cypher('resume_graph', $$
			MATCH (e:Exp)-[:USED_SKILL]->(s:Skill {id: %d})
			RETURN e.id
		$$) AS (id ag_catalog.agtype)`, skillID)

	rows, err := conn.Query(ctx, cypher)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAGEIntIDs(rows)
}

func formatCandidateData(
	exps []ExperienceRecord,
	projs []ProjectRecord,
	achvs []AchievementRecord,
	edus []EducationRecord,
	skills []SkillRecord,
	certs []CertificationRecord,
	domains []DomainRecord,
	methodologies []MethodologyRecord,
) string {
	var b strings.Builder

	b.WriteString("=== EXPERIENCES ===\n")
	for _, e := range exps {
		fmt.Fprintf(&b, "\u2022 %s at %s (%s\u2013%s)\n", e.Title, e.Company, e.StartDate, e.EndDate)
		if e.Location != "" {
			fmt.Fprintf(&b, "  Location: %s\n", e.Location)
		}
		if e.Domain != "" {
			fmt.Fprintf(&b, "  Domain: %s\n", e.Domain)
		}
		if e.Description != "" {
			fmt.Fprintf(&b, "  %s\n", e.Description)
		}
		for _, h := range e.Highlights {
			fmt.Fprintf(&b, "  - %s\n", h)
		}
	}

	if len(projs) > 0 {
		b.WriteString("\n=== PROJECTS ===\n")
		for _, p := range projs {
			fmt.Fprintf(&b, "\u2022 %s", p.Name)
			if p.URL != "" {
				fmt.Fprintf(&b, " (%s)", p.URL)
			}
			if p.ParentExperienceID != nil {
				fmt.Fprintf(&b, " [sub-project]")
			}
			b.WriteString("\n")
			if p.Description != "" {
				fmt.Fprintf(&b, "  %s\n", p.Description)
			}
			if len(p.Tech) > 0 {
				fmt.Fprintf(&b, "  Tech: %s\n", strings.Join(p.Tech, ", "))
			}
			for _, h := range p.Highlights {
				fmt.Fprintf(&b, "  - %s\n", h)
			}
		}
	}

	if len(achvs) > 0 {
		b.WriteString("\n=== KEY ACHIEVEMENTS ===\n")
		for _, a := range achvs {
			fmt.Fprintf(&b, "\u2022 %s\n", a.Text)
		}
	}

	if len(edus) > 0 {
		b.WriteString("\n=== EDUCATION ===\n")
		for _, e := range edus {
			fmt.Fprintf(&b, "\u2022 %s, %s in %s (%s\u2013%s)\n", e.Degree, e.School, e.Field, e.StartDate, e.EndDate)
		}
	}

	if len(skills) > 0 {
		b.WriteString("\n=== ALL SKILLS ===\n")
		// Group by category, mark implicit
		byCategory := make(map[string][]string)
		for _, s := range skills {
			cat := s.Category
			if cat == "" {
				cat = "other"
			}
			label := s.Name
			if s.IsImplicit {
				label += " (inferred)"
			}
			byCategory[cat] = append(byCategory[cat], label)
		}
		for cat, names := range byCategory {
			fmt.Fprintf(&b, "\u2022 %s: %s\n", cat, strings.Join(names, ", "))
		}
	}

	if len(certs) > 0 {
		b.WriteString("\n=== CERTIFICATIONS ===\n")
		for _, c := range certs {
			fmt.Fprintf(&b, "\u2022 %s", c.Name)
			if c.Issuer != "" {
				fmt.Fprintf(&b, " (%s)", c.Issuer)
			}
			if c.Year != "" {
				fmt.Fprintf(&b, " [%s]", c.Year)
			}
			b.WriteString("\n")
		}
	}

	if len(domains) > 0 {
		b.WriteString("\n=== PROFESSIONAL DOMAINS ===\n")
		for _, d := range domains {
			fmt.Fprintf(&b, "\u2022 %s\n", d.Name)
		}
	}

	if len(methodologies) > 0 {
		b.WriteString("\n=== METHODOLOGIES ===\n")
		for _, m := range methodologies {
			fmt.Fprintf(&b, "\u2022 %s", m.Name)
			if m.Description != "" {
				fmt.Fprintf(&b, ": %s", m.Description)
			}
			b.WriteString("\n")
		}
	}

	return b.String()
}

func intSetToSlice(m map[int]bool) []int {
	s := make([]int, 0, len(m))
	for id := range m {
		s = append(s, id)
	}
	return s
}
