package jobs

import (
	"context"
)

// --- Experience CRUD ---

type ExperienceRecord struct {
	ID          int      `json:"id"`
	PersonID    int      `json:"person_id"`
	Title       string   `json:"title"`
	Company     string   `json:"company"`
	Location    string   `json:"location"`
	StartDate   string   `json:"start_date"`
	EndDate     string   `json:"end_date"`
	Description string   `json:"description"`
	Highlights  []string `json:"highlights"`
	TeamSize    *int     `json:"team_size,omitempty"`
	BudgetUSD   *int     `json:"budget_usd,omitempty"`
	Domain      string   `json:"domain,omitempty"`
	IsVolunteer bool     `json:"is_volunteer,omitempty"`
}

func (db *ResumeDB) InsertExperience(ctx context.Context, personID int, e ExperienceRecord) (int, error) {
	var id int
	err := db.pool.QueryRow(ctx,
		`INSERT INTO resume_experiences (person_id, title, company, location, start_date, end_date, description, highlights)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`,
		personID, e.Title, e.Company, e.Location, e.StartDate, e.EndDate, e.Description, e.Highlights,
	).Scan(&id)
	return id, err
}

func (db *ResumeDB) GetAllExperiences(ctx context.Context, personID int) ([]ExperienceRecord, error) {
	rows, err := db.pool.Query(ctx,
		`SELECT id, person_id, title, company, location, start_date, end_date, description, highlights
		 FROM resume_experiences WHERE person_id = $1 ORDER BY id`, personID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []ExperienceRecord
	for rows.Next() {
		var r ExperienceRecord
		if err := rows.Scan(&r.ID, &r.PersonID, &r.Title, &r.Company, &r.Location,
			&r.StartDate, &r.EndDate, &r.Description, &r.Highlights); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

func (db *ResumeDB) GetExperiencesByIDs(ctx context.Context, ids []int) ([]ExperienceRecord, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := db.pool.Query(ctx,
		`SELECT id, person_id, title, company, location, start_date, end_date, description, highlights
		 FROM resume_experiences WHERE id = ANY($1) ORDER BY id`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []ExperienceRecord
	for rows.Next() {
		var r ExperienceRecord
		if err := rows.Scan(&r.ID, &r.PersonID, &r.Title, &r.Company, &r.Location,
			&r.StartDate, &r.EndDate, &r.Description, &r.Highlights); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// --- Skill CRUD ---

type SkillRecord struct {
	ID         int    `json:"id"`
	PersonID   int    `json:"person_id"`
	Name       string `json:"name"`
	Category   string `json:"category"`
	Level      string `json:"level"`
	IsImplicit bool   `json:"is_implicit,omitempty"`
	Source     string `json:"source,omitempty"` // "resume", "inferred", "enrichment"
}

func (db *ResumeDB) InsertSkill(ctx context.Context, personID int, s SkillRecord) (int, error) {
	var id int
	err := db.pool.QueryRow(ctx,
		`INSERT INTO resume_skills (person_id, name, category, level)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (person_id, name) DO UPDATE SET category = EXCLUDED.category, level = EXCLUDED.level
		 RETURNING id`,
		personID, s.Name, s.Category, s.Level,
	).Scan(&id)
	return id, err
}

func (db *ResumeDB) GetAllSkills(ctx context.Context, personID int) ([]SkillRecord, error) {
	rows, err := db.pool.Query(ctx,
		`SELECT id, person_id, name, category, level FROM resume_skills WHERE person_id = $1 ORDER BY id`, personID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SkillRecord
	for rows.Next() {
		var r SkillRecord
		if err := rows.Scan(&r.ID, &r.PersonID, &r.Name, &r.Category, &r.Level); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// --- Project CRUD ---

type ProjectRecord struct {
	ID                 int      `json:"id"`
	PersonID           int      `json:"person_id"`
	Name               string   `json:"name"`
	Description        string   `json:"description"`
	URL                string   `json:"url"`
	Tech               []string `json:"tech"`
	Highlights         []string `json:"highlights"`
	ParentExperienceID *int     `json:"parent_experience_id,omitempty"`
}

func (db *ResumeDB) InsertProject(ctx context.Context, personID int, p ProjectRecord) (int, error) {
	var id int
	err := db.pool.QueryRow(ctx,
		`INSERT INTO resume_projects (person_id, name, description, url, tech, highlights)
		 VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		personID, p.Name, p.Description, p.URL, p.Tech, p.Highlights,
	).Scan(&id)
	return id, err
}

func (db *ResumeDB) GetAllProjects(ctx context.Context, personID int) ([]ProjectRecord, error) {
	rows, err := db.pool.Query(ctx,
		`SELECT id, person_id, name, description, url, tech, highlights
		 FROM resume_projects WHERE person_id = $1 ORDER BY id`, personID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []ProjectRecord
	for rows.Next() {
		var r ProjectRecord
		if err := rows.Scan(&r.ID, &r.PersonID, &r.Name, &r.Description, &r.URL, &r.Tech, &r.Highlights); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

func (db *ResumeDB) GetProjectsByIDs(ctx context.Context, ids []int) ([]ProjectRecord, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := db.pool.Query(ctx,
		`SELECT id, person_id, name, description, url, tech, highlights
		 FROM resume_projects WHERE id = ANY($1) ORDER BY id`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []ProjectRecord
	for rows.Next() {
		var r ProjectRecord
		if err := rows.Scan(&r.ID, &r.PersonID, &r.Name, &r.Description, &r.URL, &r.Tech, &r.Highlights); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// --- Achievement CRUD ---

type AchievementRecord struct {
	ID            int      `json:"id"`
	PersonID      int      `json:"person_id"`
	Text          string   `json:"text"`
	Metric        string   `json:"metric"`
	Value         string   `json:"value"`
	Context       string   `json:"context"`
	MetricNumeric *float64 `json:"metric_numeric,omitempty"`
	MetricUnit    string   `json:"metric_unit,omitempty"`
}

func (db *ResumeDB) InsertAchievement(ctx context.Context, personID int, a AchievementRecord) (int, error) {
	var id int
	err := db.pool.QueryRow(ctx,
		`INSERT INTO resume_achievements (person_id, text, metric, value, context)
		 VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		personID, a.Text, a.Metric, a.Value, a.Context,
	).Scan(&id)
	return id, err
}

func (db *ResumeDB) GetAllAchievements(ctx context.Context, personID int) ([]AchievementRecord, error) {
	rows, err := db.pool.Query(ctx,
		`SELECT id, person_id, text, metric, value, context
		 FROM resume_achievements WHERE person_id = $1 ORDER BY id`, personID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []AchievementRecord
	for rows.Next() {
		var r AchievementRecord
		if err := rows.Scan(&r.ID, &r.PersonID, &r.Text, &r.Metric, &r.Value, &r.Context); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

func (db *ResumeDB) GetAchievementsByIDs(ctx context.Context, ids []int) ([]AchievementRecord, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := db.pool.Query(ctx,
		`SELECT id, person_id, text, metric, value, context
		 FROM resume_achievements WHERE id = ANY($1) ORDER BY id`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []AchievementRecord
	for rows.Next() {
		var r AchievementRecord
		if err := rows.Scan(&r.ID, &r.PersonID, &r.Text, &r.Metric, &r.Value, &r.Context); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// --- Education CRUD ---

type EducationRecord struct {
	ID         int      `json:"id"`
	PersonID   int      `json:"person_id"`
	School     string   `json:"school"`
	Degree     string   `json:"degree"`
	Field      string   `json:"field"`
	StartDate  string   `json:"start_date"`
	EndDate    string   `json:"end_date"`
	GPA        string   `json:"gpa"`
	Highlights []string `json:"highlights"`
}

func (db *ResumeDB) InsertEducation(ctx context.Context, personID int, e EducationRecord) (int, error) {
	var id int
	err := db.pool.QueryRow(ctx,
		`INSERT INTO resume_educations (person_id, school, degree, field, start_date, end_date, gpa, highlights)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`,
		personID, e.School, e.Degree, e.Field, e.StartDate, e.EndDate, e.GPA, e.Highlights,
	).Scan(&id)
	return id, err
}

func (db *ResumeDB) GetAllEducations(ctx context.Context, personID int) ([]EducationRecord, error) {
	rows, err := db.pool.Query(ctx,
		`SELECT id, person_id, school, degree, field, start_date, end_date, gpa, highlights
		 FROM resume_educations WHERE person_id = $1 ORDER BY id`, personID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []EducationRecord
	for rows.Next() {
		var r EducationRecord
		if err := rows.Scan(&r.ID, &r.PersonID, &r.School, &r.Degree, &r.Field,
			&r.StartDate, &r.EndDate, &r.GPA, &r.Highlights); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// --- Certification CRUD ---

type CertificationRecord struct {
	ID       int    `json:"id"`
	PersonID int    `json:"person_id"`
	Name     string `json:"name"`
	Issuer   string `json:"issuer"`
	Year     string `json:"year"`
	URL      string `json:"url"`
}

func (db *ResumeDB) InsertCertification(ctx context.Context, personID int, c CertificationRecord) (int, error) {
	var id int
	err := db.pool.QueryRow(ctx,
		`INSERT INTO resume_certifications (person_id, name, issuer, year, url)
		 VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		personID, c.Name, c.Issuer, c.Year, c.URL,
	).Scan(&id)
	return id, err
}

func (db *ResumeDB) GetAllCertifications(ctx context.Context, personID int) ([]CertificationRecord, error) {
	rows, err := db.pool.Query(ctx,
		`SELECT id, person_id, name, issuer, year, url
		 FROM resume_certifications WHERE person_id = $1 ORDER BY id`, personID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []CertificationRecord
	for rows.Next() {
		var r CertificationRecord
		if err := rows.Scan(&r.ID, &r.PersonID, &r.Name, &r.Issuer, &r.Year, &r.URL); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// --- Domain CRUD ---

type DomainRecord struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func (db *ResumeDB) InsertDomain(ctx context.Context, personID int, name string) (int, error) {
	var id int
	err := db.pool.QueryRow(ctx,
		`INSERT INTO public.resume_domains (person_id, name) VALUES ($1, $2)
		 ON CONFLICT (person_id, name) DO UPDATE SET name = EXCLUDED.name
		 RETURNING id`,
		personID, name,
	).Scan(&id)
	return id, err
}

func (db *ResumeDB) GetAllDomains(ctx context.Context, personID int) ([]DomainRecord, error) {
	rows, err := db.pool.Query(ctx,
		`SELECT id, name FROM public.resume_domains WHERE person_id = $1 ORDER BY id`, personID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var results []DomainRecord
	for rows.Next() {
		var r DomainRecord
		if err := rows.Scan(&r.ID, &r.Name); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// --- Methodology CRUD ---

type MethodologyRecord struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

func (db *ResumeDB) InsertMethodology(ctx context.Context, personID int, name, desc string) (int, error) {
	var id int
	err := db.pool.QueryRow(ctx,
		`INSERT INTO public.resume_methodologies (person_id, name, description) VALUES ($1, $2, $3)
		 ON CONFLICT (person_id, name) DO UPDATE SET description = EXCLUDED.description
		 RETURNING id`,
		personID, name, desc,
	).Scan(&id)
	return id, err
}

func (db *ResumeDB) GetAllMethodologies(ctx context.Context, personID int) ([]MethodologyRecord, error) {
	rows, err := db.pool.Query(ctx,
		`SELECT id, name, COALESCE(description, '') FROM public.resume_methodologies WHERE person_id = $1 ORDER BY id`, personID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var results []MethodologyRecord
	for rows.Next() {
		var r MethodologyRecord
		if err := rows.Scan(&r.ID, &r.Name, &r.Description); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// --- Extended mutations ---

// UpdateExperienceMeta updates the extended metadata on an experience row.
func (db *ResumeDB) UpdateExperienceMeta(ctx context.Context, expID int, teamSize, budgetUSD *int, domain string, isVolunteer bool) error {
	_, err := db.pool.Exec(ctx,
		`UPDATE resume_experiences SET team_size = $2, budget_usd = $3, domain = $4, is_volunteer = $5 WHERE id = $1`,
		expID, teamSize, budgetUSD, domain, isVolunteer,
	)
	return err
}

// InsertProjectWithParent inserts a project linked to a parent experience.
func (db *ResumeDB) InsertProjectWithParent(ctx context.Context, personID int, parentExpID *int, p ProjectRecord) (int, error) {
	var id int
	err := db.pool.QueryRow(ctx,
		`INSERT INTO resume_projects (person_id, name, description, url, tech, highlights, parent_experience_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		personID, p.Name, p.Description, p.URL, p.Tech, p.Highlights, parentExpID,
	).Scan(&id)
	return id, err
}

// MarkPersonEnriched sets the enriched_at timestamp on a person.
func (db *ResumeDB) MarkPersonEnriched(ctx context.Context, personID int) error {
	_, err := db.pool.Exec(ctx,
		`UPDATE resume_persons SET enriched_at = now() WHERE id = $1`, personID)
	return err
}

// InsertSkillExtended inserts a skill with implicit/source tracking.
func (db *ResumeDB) InsertSkillExtended(ctx context.Context, personID int, s SkillRecord) (int, error) {
	var id int
	err := db.pool.QueryRow(ctx,
		`INSERT INTO resume_skills (person_id, name, category, level, is_implicit, source)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (person_id, name) DO UPDATE SET category = EXCLUDED.category, level = EXCLUDED.level, is_implicit = EXCLUDED.is_implicit, source = EXCLUDED.source
		 RETURNING id`,
		personID, s.Name, s.Category, s.Level, s.IsImplicit, s.Source,
	).Scan(&id)
	return id, err
}

// InsertAchievementExtended inserts an achievement with parsed metric fields.
func (db *ResumeDB) InsertAchievementExtended(ctx context.Context, personID int, a AchievementRecord) (int, error) {
	var id int
	err := db.pool.QueryRow(ctx,
		`INSERT INTO resume_achievements (person_id, text, metric, value, context, metric_numeric, metric_unit)
		 VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		personID, a.Text, a.Metric, a.Value, a.Context, a.MetricNumeric, a.MetricUnit,
	).Scan(&id)
	return id, err
}

