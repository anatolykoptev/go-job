package jobs

import (
	"context"
	"errors"
	"strings"
)

// --- Output types ---

// ExperienceSummary is a compact view of an experience for profile output.
type ExperienceSummary struct {
	ID        int      `json:"id"`
	Title     string   `json:"title"`
	Company   string   `json:"company"`
	Location  string   `json:"location,omitempty"`
	StartDate string   `json:"start_date"`
	EndDate   string   `json:"end_date"`
	Domain    string   `json:"domain,omitempty"`
	Highlights []string `json:"highlights,omitempty"`
}

// SkillSummary is a compact view of a skill for profile output.
type SkillSummary struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
	Level    string `json:"level"`
}

// ProjectSummary is a compact view of a project for profile output.
type ProjectSummary struct {
	ID   int      `json:"id"`
	Name string   `json:"name"`
	Tech []string `json:"tech,omitempty"`
	URL  string   `json:"url,omitempty"`
}

// AchievementSummary is a compact view of an achievement for profile output.
type AchievementSummary struct {
	ID      int    `json:"id"`
	Text    string `json:"text"`
	Metric  string `json:"metric,omitempty"`
	Value   string `json:"value,omitempty"`
	Context string `json:"context,omitempty"`
}

// EducationSummary is a compact view of an education entry for profile output.
type EducationSummary struct {
	ID        int    `json:"id"`
	School    string `json:"school"`
	Degree    string `json:"degree"`
	Field     string `json:"field,omitempty"`
	StartDate string `json:"start_date,omitempty"`
	EndDate   string `json:"end_date,omitempty"`
}

// CertificationSummary is a compact view of a certification for profile output.
type CertificationSummary struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Issuer string `json:"issuer,omitempty"`
	Year   string `json:"year,omitempty"`
}

// ResumeProfileResult is the structured output of resume_profile.
type ResumeProfileResult struct {
	PersonID   int               `json:"person_id"`
	Name       string            `json:"name"`
	Email      string            `json:"email,omitempty"`
	Location   string            `json:"location,omitempty"`
	Links      map[string]string `json:"links,omitempty"`
	Summary    string            `json:"summary,omitempty"`
	EnrichedAt string            `json:"enriched_at,omitempty"`

	Experiences    []ExperienceSummary    `json:"experiences,omitempty"`
	Skills         []SkillSummary         `json:"skills,omitempty"`
	Projects       []ProjectSummary       `json:"projects,omitempty"`
	Achievements   []AchievementSummary   `json:"achievements,omitempty"`
	Educations     []EducationSummary     `json:"educations,omitempty"`
	Certifications []CertificationSummary `json:"certifications,omitempty"`
	Domains        []string               `json:"domains,omitempty"`
	Methodologies  []string               `json:"methodologies,omitempty"`

	Stats struct {
		TotalExperiences int `json:"total_experiences"`
		TotalSkills      int `json:"total_skills"`
		TotalProjects    int `json:"total_projects"`
		VectorsStored    int `json:"vectors_stored"`
	} `json:"stats"`
}

// GetResumeProfile reads the full resume profile from PostgreSQL.
// If section is non-empty, only that section is loaded.
func GetResumeProfile(ctx context.Context, section string) (*ResumeProfileResult, error) {
	db := GetResumeDB()
	if db == nil {
		return nil, errors.New("resume database not configured (set DATABASE_URL)")
	}

	personID := db.GetLatestPersonID(ctx)
	if personID == 0 {
		return nil, errors.New("no resume found — use master_resume_build first")
	}

	person, err := db.GetPerson(ctx, personID)
	if err != nil {
		return nil, errors.New("no resume found — use master_resume_build first")
	}

	result := &ResumeProfileResult{
		PersonID:   person.ID,
		Name:       person.Name,
		Email:      person.Email,
		Location:   person.Location,
		Links:      person.Links,
		Summary:    person.Summary,
		EnrichedAt: db.GetPersonEnrichedAt(ctx, personID),
	}

	sec := strings.ToLower(strings.TrimSpace(section))

	if sec == "" || sec == "experiences" {
		result.Experiences = loadExperiences(ctx, db, personID)
		result.Stats.TotalExperiences = len(result.Experiences)
	}
	if sec == "" || sec == "skills" {
		result.Skills = loadSkills(ctx, db, personID)
		result.Stats.TotalSkills = len(result.Skills)
	}
	if sec == "" || sec == "projects" {
		result.Projects = loadProjects(ctx, db, personID)
		result.Stats.TotalProjects = len(result.Projects)
	}
	if sec == "" || sec == "achievements" {
		result.Achievements = loadAchievements(ctx, db, personID)
	}
	if sec == "" || sec == "educations" {
		result.Educations = loadEducations(ctx, db, personID)
	}
	if sec == "" || sec == "certifications" {
		result.Certifications = loadCertifications(ctx, db, personID)
	}
	if sec == "" || sec == "domains" {
		result.Domains = loadDomains(ctx, db, personID)
	}
	if sec == "" || sec == "methodologies" {
		result.Methodologies = loadMethodologies(ctx, db, personID)
	}

	// Approximate vector count via MemDB search (no dedicated count API).
	if sec == "" {
		if mdb := GetMemDB(); mdb != nil {
			all, err := mdb.Search(ctx, "resume experience project skill achievement", 100, 0.0)
			if err == nil {
				result.Stats.VectorsStored = len(all)
			}
		}
	}

	return result, nil
}

func loadExperiences(ctx context.Context, db *ResumeDB, personID int) []ExperienceSummary {
	records, err := db.GetAllExperiences(ctx, personID)
	if err != nil {
		return nil
	}
	out := make([]ExperienceSummary, 0, len(records))
	for _, r := range records {
		out = append(out, ExperienceSummary{
			ID:         r.ID,
			Title:      r.Title,
			Company:    r.Company,
			Location:   r.Location,
			StartDate:  r.StartDate,
			EndDate:    r.EndDate,
			Domain:     r.Domain,
			Highlights: r.Highlights,
		})
	}
	return out
}

func loadSkills(ctx context.Context, db *ResumeDB, personID int) []SkillSummary {
	records, err := db.GetAllSkills(ctx, personID)
	if err != nil {
		return nil
	}
	out := make([]SkillSummary, 0, len(records))
	for _, r := range records {
		out = append(out, SkillSummary{
			ID:       r.ID,
			Name:     r.Name,
			Category: r.Category,
			Level:    r.Level,
		})
	}
	return out
}

func loadProjects(ctx context.Context, db *ResumeDB, personID int) []ProjectSummary {
	records, err := db.GetAllProjects(ctx, personID)
	if err != nil {
		return nil
	}
	out := make([]ProjectSummary, 0, len(records))
	for _, r := range records {
		out = append(out, ProjectSummary{
			ID:   r.ID,
			Name: r.Name,
			Tech: r.Tech,
			URL:  r.URL,
		})
	}
	return out
}

func loadAchievements(ctx context.Context, db *ResumeDB, personID int) []AchievementSummary {
	records, err := db.GetAllAchievements(ctx, personID)
	if err != nil {
		return nil
	}
	out := make([]AchievementSummary, 0, len(records))
	for _, r := range records {
		out = append(out, AchievementSummary{
			ID:      r.ID,
			Text:    r.Text,
			Metric:  r.Metric,
			Value:   r.Value,
			Context: r.Context,
		})
	}
	return out
}

func loadEducations(ctx context.Context, db *ResumeDB, personID int) []EducationSummary {
	records, err := db.GetAllEducations(ctx, personID)
	if err != nil {
		return nil
	}
	out := make([]EducationSummary, 0, len(records))
	for _, r := range records {
		out = append(out, EducationSummary{
			ID:        r.ID,
			School:    r.School,
			Degree:    r.Degree,
			Field:     r.Field,
			StartDate: r.StartDate,
			EndDate:   r.EndDate,
		})
	}
	return out
}

func loadCertifications(ctx context.Context, db *ResumeDB, personID int) []CertificationSummary {
	records, err := db.GetAllCertifications(ctx, personID)
	if err != nil {
		return nil
	}
	out := make([]CertificationSummary, 0, len(records))
	for _, r := range records {
		out = append(out, CertificationSummary{
			ID:     r.ID,
			Name:   r.Name,
			Issuer: r.Issuer,
			Year:   r.Year,
		})
	}
	return out
}

func loadDomains(ctx context.Context, db *ResumeDB, personID int) []string {
	records, err := db.GetAllDomains(ctx, personID)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(records))
	for _, r := range records {
		out = append(out, r.Name)
	}
	return out
}

func loadMethodologies(ctx context.Context, db *ResumeDB, personID int) []string {
	records, err := db.GetAllMethodologies(ctx, personID)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(records))
	for _, r := range records {
		out = append(out, r.Name)
	}
	return out
}
