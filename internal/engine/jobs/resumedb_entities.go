package jobs

import (
	"context"
)

// Account ownership (plan ADR-6): every child-table statement carries a
// person-ownership predicate — personOwnedBy* constants live in
// resumedb_edit.go — or an explicit EXISTS bound to the caller's account, so a
// foreign account's rows are unreachable: reads return empty/not-found,
// inserts fail with ErrNoRows (the SELECT finds no owned parent), and
// updates/deletes affect zero rows.

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

func (a *ResumeAccount) InsertExperience(ctx context.Context, personID int, e ExperienceRecord) (int, error) {
	if !a.writable() {
		return 0, ErrNoAccountScope
	}
	var id int
	err := a.conn(ctx).QueryRow(ctx,
		`INSERT INTO resume_experiences (person_id, title, company, location, start_date, end_date, description, highlights)
		 SELECT $1, $2, $3, $4, $5, $6, $7, $8
		 WHERE EXISTS (SELECT 1 FROM resume_persons WHERE id = $1 AND account_id = $9)
		 RETURNING id`,
		personID, e.Title, e.Company, e.Location, e.StartDate, e.EndDate, e.Description, e.Highlights, a.aid,
	).Scan(&id)
	return id, err
}

func (a *ResumeAccount) GetAllExperiences(ctx context.Context, personID int) ([]ExperienceRecord, error) {
	rows, err := a.conn(ctx).Query(ctx,
		`SELECT id, COALESCE(person_id, 0), title, company, COALESCE(location, ''),
		        COALESCE(start_date, ''), COALESCE(end_date, ''), COALESCE(description, ''), highlights,
		        COALESCE(domain, ''), team_size, budget_usd, COALESCE(is_volunteer, false)
		 FROM resume_experiences
		 WHERE person_id = $1
		   AND EXISTS (SELECT 1 FROM resume_persons WHERE id = $1 AND account_id = $2)
		 ORDER BY id`, personID, a.aid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []ExperienceRecord
	for rows.Next() {
		var r ExperienceRecord
		if err := rows.Scan(&r.ID, &r.PersonID, &r.Title, &r.Company, &r.Location,
			&r.StartDate, &r.EndDate, &r.Description, &r.Highlights, &r.Domain,
			&r.TeamSize, &r.BudgetUSD, &r.IsVolunteer); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

func (a *ResumeAccount) GetExperiencesByIDs(ctx context.Context, ids []int) ([]ExperienceRecord, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := a.db.pool.Query(ctx,
		`SELECT id, COALESCE(person_id, 0), title, company, COALESCE(location, ''),
		        COALESCE(start_date, ''), COALESCE(end_date, ''), COALESCE(description, ''), highlights,
		        COALESCE(domain, '')
		 FROM resume_experiences WHERE id = ANY($1) AND `+personOwnedBy2+` ORDER BY id`, ids, a.aid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []ExperienceRecord
	for rows.Next() {
		var r ExperienceRecord
		if err := rows.Scan(&r.ID, &r.PersonID, &r.Title, &r.Company, &r.Location,
			&r.StartDate, &r.EndDate, &r.Description, &r.Highlights, &r.Domain); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// GetExperienceByID fetches a single experience row by primary key — empty
// result (ErrNoRows) when the row belongs to another account's person.
func (a *ResumeAccount) GetExperienceByID(ctx context.Context, expID int) (ExperienceRecord, error) {
	var r ExperienceRecord
	err := a.conn(ctx).QueryRow(ctx,
		`SELECT id, COALESCE(person_id, 0), title, company, COALESCE(location, ''),
		        COALESCE(start_date, ''), COALESCE(end_date, ''), COALESCE(description, ''), highlights,
		        COALESCE(domain, '')
		 FROM resume_experiences WHERE id = $1 AND `+personOwnedBy2, expID, a.aid).
		Scan(&r.ID, &r.PersonID, &r.Title, &r.Company, &r.Location,
			&r.StartDate, &r.EndDate, &r.Description, &r.Highlights, &r.Domain)
	return r, err
}

// UpdateExperience updates the editable columns of an experience row owned by
// the bound account; a foreign id is a silent no-op.
func (a *ResumeAccount) UpdateExperience(ctx context.Context, expID int, e ExperienceRecord) error {
	if !a.writable() {
		return ErrNoAccountScope
	}
	_, err := a.conn(ctx).Exec(ctx,
		`UPDATE resume_experiences
		 SET title = $2, company = $3, location = $4, start_date = $5, end_date = $6, description = $7,
		     highlights = $8, updated_at = now()
		 WHERE id = $1 AND `+personOwnedBy2,
		expID, e.Title, e.Company, e.Location, e.StartDate, e.EndDate, e.Description, e.Highlights, a.aid)
	return err
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

func (a *ResumeAccount) InsertSkill(ctx context.Context, personID int, s SkillRecord) (int, error) {
	if !a.writable() {
		return 0, ErrNoAccountScope
	}
	var id int
	err := a.db.pool.QueryRow(ctx,
		`INSERT INTO resume_skills (person_id, name, category, level)
		 SELECT $1, $2, $3, $4
		 WHERE EXISTS (SELECT 1 FROM resume_persons WHERE id = $1 AND account_id = $5)
		 ON CONFLICT (person_id, name) DO UPDATE SET category = EXCLUDED.category, level = EXCLUDED.level
		 RETURNING id`,
		personID, s.Name, s.Category, s.Level, a.aid,
	).Scan(&id)
	return id, err
}

func (a *ResumeAccount) GetAllSkills(ctx context.Context, personID int) ([]SkillRecord, error) {
	rows, err := a.conn(ctx).Query(ctx,
		`SELECT id, COALESCE(person_id, 0), name, COALESCE(category, ''), COALESCE(level, ''),
		        COALESCE(is_implicit, false), COALESCE(source, '')
		 FROM resume_skills
		 WHERE person_id = $1
		   AND EXISTS (SELECT 1 FROM resume_persons WHERE id = $1 AND account_id = $2)
		 ORDER BY id`, personID, a.aid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SkillRecord
	for rows.Next() {
		var r SkillRecord
		if err := rows.Scan(&r.ID, &r.PersonID, &r.Name, &r.Category, &r.Level, &r.IsImplicit, &r.Source); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// GetSkillByID fetches a single skill row by primary key — ErrNoRows when the
// row belongs to another account's person.
func (a *ResumeAccount) GetSkillByID(ctx context.Context, skillID int) (SkillRecord, error) {
	var r SkillRecord
	err := a.conn(ctx).QueryRow(ctx,
		`SELECT id, COALESCE(person_id, 0), name, COALESCE(category, ''), COALESCE(level, '')
		 FROM resume_skills WHERE id = $1 AND `+personOwnedBy2, skillID, a.aid).
		Scan(&r.ID, &r.PersonID, &r.Name, &r.Category, &r.Level)
	return r, err
}

// UpdateSkill updates the editable columns of a skill row owned by the bound
// account; a foreign id is a silent no-op.
func (a *ResumeAccount) UpdateSkill(ctx context.Context, skillID int, s SkillRecord) error {
	if !a.writable() {
		return ErrNoAccountScope
	}
	_, err := a.conn(ctx).Exec(ctx,
		`UPDATE resume_skills SET name = $2, category = $3, level = $4, updated_at = now()
		 WHERE id = $1 AND `+personOwnedBy5,
		skillID, s.Name, s.Category, s.Level, a.aid)
	return err
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

func (a *ResumeAccount) InsertProject(ctx context.Context, personID int, p ProjectRecord) (int, error) {
	if !a.writable() {
		return 0, ErrNoAccountScope
	}
	var id int
	err := a.conn(ctx).QueryRow(ctx,
		`INSERT INTO resume_projects (person_id, name, description, url, tech, highlights)
		 SELECT $1, $2, $3, $4, $5, $6
		 WHERE EXISTS (SELECT 1 FROM resume_persons WHERE id = $1 AND account_id = $7)
		 RETURNING id`,
		personID, p.Name, p.Description, p.URL, p.Tech, p.Highlights, a.aid,
	).Scan(&id)
	return id, err
}

func (a *ResumeAccount) GetAllProjects(ctx context.Context, personID int) ([]ProjectRecord, error) {
	rows, err := a.conn(ctx).Query(ctx,
		`SELECT id, COALESCE(person_id, 0), name, COALESCE(description, ''), COALESCE(url, ''), tech, highlights,
		        parent_experience_id
		 FROM resume_projects
		 WHERE person_id = $1
		   AND EXISTS (SELECT 1 FROM resume_persons WHERE id = $1 AND account_id = $2)
		 ORDER BY id`, personID, a.aid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []ProjectRecord
	for rows.Next() {
		var r ProjectRecord
		if err := rows.Scan(&r.ID, &r.PersonID, &r.Name, &r.Description, &r.URL, &r.Tech, &r.Highlights,
			&r.ParentExperienceID); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

func (a *ResumeAccount) GetProjectsByIDs(ctx context.Context, ids []int) ([]ProjectRecord, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := a.db.pool.Query(ctx,
		`SELECT id, COALESCE(person_id, 0), name, COALESCE(description, ''), COALESCE(url, ''), tech, highlights
		 FROM resume_projects WHERE id = ANY($1) AND `+personOwnedBy2+` ORDER BY id`, ids, a.aid)
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

// GetProjectByID fetches a single project row by primary key — ErrNoRows when
// the row belongs to another account's person.
func (a *ResumeAccount) GetProjectByID(ctx context.Context, projectID int) (ProjectRecord, error) {
	var r ProjectRecord
	err := a.conn(ctx).QueryRow(ctx,
		`SELECT id, COALESCE(person_id, 0), name, COALESCE(description, ''), COALESCE(url, ''), tech, highlights
		 FROM resume_projects WHERE id = $1 AND `+personOwnedBy2, projectID, a.aid).
		Scan(&r.ID, &r.PersonID, &r.Name, &r.Description, &r.URL, &r.Tech, &r.Highlights)
	return r, err
}

// UpdateProject updates the editable columns of a project row owned by the
// bound account; a foreign id is a silent no-op.
func (a *ResumeAccount) UpdateProject(ctx context.Context, projectID int, p ProjectRecord) error {
	if !a.writable() {
		return ErrNoAccountScope
	}
	_, err := a.conn(ctx).Exec(ctx,
		`UPDATE resume_projects SET name = $2, description = $3, url = $4, tech = $5, highlights = $6, updated_at = now()
		 WHERE id = $1 AND `+personOwnedBy7,
		projectID, p.Name, p.Description, p.URL, p.Tech, p.Highlights, a.aid)
	return err
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

func (a *ResumeAccount) InsertAchievement(ctx context.Context, personID int, ach AchievementRecord) (int, error) {
	if !a.writable() {
		return 0, ErrNoAccountScope
	}
	var id int
	err := a.conn(ctx).QueryRow(ctx,
		`INSERT INTO resume_achievements (person_id, text, metric, value, context)
		 SELECT $1, $2, $3, $4, $5
		 WHERE EXISTS (SELECT 1 FROM resume_persons WHERE id = $1 AND account_id = $6)
		 RETURNING id`,
		personID, ach.Text, ach.Metric, ach.Value, ach.Context, a.aid,
	).Scan(&id)
	return id, err
}

func (a *ResumeAccount) GetAllAchievements(ctx context.Context, personID int) ([]AchievementRecord, error) {
	rows, err := a.conn(ctx).Query(ctx,
		`SELECT id, COALESCE(person_id, 0), text, COALESCE(metric, ''), COALESCE(value, ''), COALESCE(context, '')
		 FROM resume_achievements
		 WHERE person_id = $1
		   AND EXISTS (SELECT 1 FROM resume_persons WHERE id = $1 AND account_id = $2)
		 ORDER BY id`, personID, a.aid)
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

func (a *ResumeAccount) GetAchievementsByIDs(ctx context.Context, ids []int) ([]AchievementRecord, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := a.db.pool.Query(ctx,
		`SELECT id, COALESCE(person_id, 0), text, COALESCE(metric, ''), COALESCE(value, ''), COALESCE(context, '')
		 FROM resume_achievements WHERE id = ANY($1) AND `+personOwnedBy2+` ORDER BY id`, ids, a.aid)
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

// GetAchievementByID fetches a single achievement row by primary key —
// ErrNoRows when the row belongs to another account's person.
func (a *ResumeAccount) GetAchievementByID(ctx context.Context, achvID int) (AchievementRecord, error) {
	var r AchievementRecord
	err := a.conn(ctx).QueryRow(ctx,
		`SELECT id, COALESCE(person_id, 0), text, COALESCE(metric, ''), COALESCE(value, ''), COALESCE(context, '')
		 FROM resume_achievements WHERE id = $1 AND `+personOwnedBy2, achvID, a.aid).
		Scan(&r.ID, &r.PersonID, &r.Text, &r.Metric, &r.Value, &r.Context)
	return r, err
}

// UpdateAchievement updates the editable columns of an achievement row owned
// by the bound account; a foreign id is a silent no-op.
func (a *ResumeAccount) UpdateAchievement(ctx context.Context, achvID int, ach AchievementRecord) error {
	if !a.writable() {
		return ErrNoAccountScope
	}
	_, err := a.conn(ctx).Exec(ctx,
		`UPDATE resume_achievements SET text = $2, metric = $3, value = $4, context = $5, updated_at = now()
		 WHERE id = $1 AND `+personOwnedBy6,
		achvID, ach.Text, ach.Metric, ach.Value, ach.Context, a.aid)
	return err
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

func (a *ResumeAccount) InsertEducation(ctx context.Context, personID int, e EducationRecord) (int, error) {
	if !a.writable() {
		return 0, ErrNoAccountScope
	}
	var id int
	err := a.conn(ctx).QueryRow(ctx,
		`INSERT INTO resume_educations (person_id, school, degree, field, start_date, end_date, gpa, highlights)
		 SELECT $1, $2, $3, $4, $5, $6, $7, $8
		 WHERE EXISTS (SELECT 1 FROM resume_persons WHERE id = $1 AND account_id = $9)
		 RETURNING id`,
		personID, e.School, e.Degree, e.Field, e.StartDate, e.EndDate, e.GPA, e.Highlights, a.aid,
	).Scan(&id)
	return id, err
}

func (a *ResumeAccount) GetAllEducations(ctx context.Context, personID int) ([]EducationRecord, error) {
	rows, err := a.conn(ctx).Query(ctx,
		`SELECT id, COALESCE(person_id, 0), school, degree, COALESCE(field, ''),
		        COALESCE(start_date, ''), COALESCE(end_date, ''), COALESCE(gpa, ''), highlights
		 FROM resume_educations
		 WHERE person_id = $1
		   AND EXISTS (SELECT 1 FROM resume_persons WHERE id = $1 AND account_id = $2)
		 ORDER BY id`, personID, a.aid)
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

// GetEducationByID fetches a single education row by primary key — ErrNoRows
// when the row belongs to another account's person.
func (a *ResumeAccount) GetEducationByID(ctx context.Context, eduID int) (EducationRecord, error) {
	var r EducationRecord
	err := a.conn(ctx).QueryRow(ctx,
		`SELECT id, COALESCE(person_id, 0), school, degree, COALESCE(field, ''),
		        COALESCE(start_date, ''), COALESCE(end_date, ''), COALESCE(gpa, ''), highlights
		 FROM resume_educations WHERE id = $1 AND `+personOwnedBy2, eduID, a.aid).
		Scan(&r.ID, &r.PersonID, &r.School, &r.Degree, &r.Field,
			&r.StartDate, &r.EndDate, &r.GPA, &r.Highlights)
	return r, err
}

// UpdateEducation updates the editable columns of an education row owned by
// the bound account; a foreign id is a silent no-op.
func (a *ResumeAccount) UpdateEducation(ctx context.Context, eduID int, e EducationRecord) error {
	if !a.writable() {
		return ErrNoAccountScope
	}
	_, err := a.conn(ctx).Exec(ctx,
		`UPDATE resume_educations SET school = $2, degree = $3, field = $4, start_date = $5, end_date = $6,
		     gpa = $7, highlights = $8, updated_at = now()
		 WHERE id = $1 AND `+personOwnedBy9,
		eduID, e.School, e.Degree, e.Field, e.StartDate, e.EndDate, e.GPA, e.Highlights, a.aid)
	return err
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

func (a *ResumeAccount) InsertCertification(ctx context.Context, personID int, c CertificationRecord) (int, error) {
	if !a.writable() {
		return 0, ErrNoAccountScope
	}
	var id int
	err := a.conn(ctx).QueryRow(ctx,
		`INSERT INTO resume_certifications (person_id, name, issuer, year, url)
		 SELECT $1, $2, $3, $4, $5
		 WHERE EXISTS (SELECT 1 FROM resume_persons WHERE id = $1 AND account_id = $6)
		 RETURNING id`,
		personID, c.Name, c.Issuer, c.Year, c.URL, a.aid,
	).Scan(&id)
	return id, err
}

func (a *ResumeAccount) GetAllCertifications(ctx context.Context, personID int) ([]CertificationRecord, error) {
	rows, err := a.conn(ctx).Query(ctx,
		`SELECT id, COALESCE(person_id, 0), name, COALESCE(issuer, ''), COALESCE(year, ''), COALESCE(url, '')
		 FROM resume_certifications
		 WHERE person_id = $1
		   AND EXISTS (SELECT 1 FROM resume_persons WHERE id = $1 AND account_id = $2)
		 ORDER BY id`, personID, a.aid)
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

// GetCertificationByID fetches a single certification row by primary key —
// ErrNoRows when the row belongs to another account's person.
func (a *ResumeAccount) GetCertificationByID(ctx context.Context, certID int) (CertificationRecord, error) {
	var r CertificationRecord
	err := a.conn(ctx).QueryRow(ctx,
		`SELECT id, COALESCE(person_id, 0), name, COALESCE(issuer, ''), COALESCE(year, ''), COALESCE(url, '')
		 FROM resume_certifications WHERE id = $1 AND `+personOwnedBy2, certID, a.aid).
		Scan(&r.ID, &r.PersonID, &r.Name, &r.Issuer, &r.Year, &r.URL)
	return r, err
}

// UpdateCertification updates the editable columns of a certification row
// owned by the bound account; a foreign id is a silent no-op.
func (a *ResumeAccount) UpdateCertification(ctx context.Context, certID int, c CertificationRecord) error {
	if !a.writable() {
		return ErrNoAccountScope
	}
	_, err := a.conn(ctx).Exec(ctx,
		`UPDATE resume_certifications SET name = $2, issuer = $3, year = $4, url = $5, updated_at = now()
		 WHERE id = $1 AND `+personOwnedBy6,
		certID, c.Name, c.Issuer, c.Year, c.URL, a.aid)
	return err
}

// --- Domain CRUD ---

type DomainRecord struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func (a *ResumeAccount) InsertDomain(ctx context.Context, personID int, name string) (int, error) {
	if !a.writable() {
		return 0, ErrNoAccountScope
	}
	var id int
	err := a.conn(ctx).QueryRow(ctx,
		`INSERT INTO public.resume_domains (person_id, name)
		 SELECT $1, $2 WHERE EXISTS (SELECT 1 FROM resume_persons WHERE id = $1 AND account_id = $3)
		 ON CONFLICT (person_id, name) DO UPDATE SET name = EXCLUDED.name
		 RETURNING id`,
		personID, name, a.aid,
	).Scan(&id)
	return id, err
}

func (a *ResumeAccount) GetAllDomains(ctx context.Context, personID int) ([]DomainRecord, error) {
	rows, err := a.conn(ctx).Query(ctx,
		`SELECT id, name FROM public.resume_domains
		 WHERE person_id = $1
		   AND EXISTS (SELECT 1 FROM resume_persons WHERE id = $1 AND account_id = $2)
		 ORDER BY id`, personID, a.aid)
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

// GetDomainByID fetches a single domain row by primary key — ErrNoRows when
// the row belongs to another account's person.
func (a *ResumeAccount) GetDomainByID(ctx context.Context, domainID int) (DomainRecord, error) {
	var r DomainRecord
	err := a.conn(ctx).QueryRow(ctx,
		`SELECT id, name FROM public.resume_domains WHERE id = $1 AND `+personOwnedBy2, domainID, a.aid).
		Scan(&r.ID, &r.Name)
	return r, err
}

// UpdateDomain updates the name of a domain row owned by the bound account; a
// foreign id is a silent no-op.
func (a *ResumeAccount) UpdateDomain(ctx context.Context, domainID int, name string) error {
	if !a.writable() {
		return ErrNoAccountScope
	}
	_, err := a.conn(ctx).Exec(ctx,
		`UPDATE public.resume_domains SET name = $2 WHERE id = $1 AND `+personOwnedBy3,
		domainID, name, a.aid)
	return err
}

// --- Methodology CRUD ---

type MethodologyRecord struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

func (a *ResumeAccount) InsertMethodology(ctx context.Context, personID int, name, desc string) (int, error) {
	if !a.writable() {
		return 0, ErrNoAccountScope
	}
	var id int
	err := a.conn(ctx).QueryRow(ctx,
		`INSERT INTO public.resume_methodologies (person_id, name, description)
		 SELECT $1, $2, $3 WHERE EXISTS (SELECT 1 FROM resume_persons WHERE id = $1 AND account_id = $4)
		 ON CONFLICT (person_id, name) DO UPDATE SET description = EXCLUDED.description
		 RETURNING id`,
		personID, name, desc, a.aid,
	).Scan(&id)
	return id, err
}

func (a *ResumeAccount) GetAllMethodologies(ctx context.Context, personID int) ([]MethodologyRecord, error) {
	rows, err := a.conn(ctx).Query(ctx,
		`SELECT id, name, COALESCE(description, '') FROM public.resume_methodologies
		 WHERE person_id = $1
		   AND EXISTS (SELECT 1 FROM resume_persons WHERE id = $1 AND account_id = $2)
		 ORDER BY id`, personID, a.aid)
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

// GetMethodologyByID fetches a single methodology row by primary key —
// ErrNoRows when the row belongs to another account's person.
func (a *ResumeAccount) GetMethodologyByID(ctx context.Context, methodID int) (MethodologyRecord, error) {
	var r MethodologyRecord
	err := a.conn(ctx).QueryRow(ctx,
		`SELECT id, name, COALESCE(description, '') FROM public.resume_methodologies WHERE id = $1 AND `+personOwnedBy2, methodID, a.aid).
		Scan(&r.ID, &r.Name, &r.Description)
	return r, err
}

// UpdateMethodology updates the name and description of a methodology row
// owned by the bound account; a foreign id is a silent no-op.
func (a *ResumeAccount) UpdateMethodology(ctx context.Context, methodID int, name, desc string) error {
	if !a.writable() {
		return ErrNoAccountScope
	}
	_, err := a.conn(ctx).Exec(ctx,
		`UPDATE public.resume_methodologies SET name = $2, description = $3 WHERE id = $1 AND `+personOwnedBy4,
		methodID, name, desc, a.aid)
	return err
}

// --- Extended mutations ---

// UpdateExperienceMeta updates the extended metadata on an experience row
// owned by the bound account; a foreign id is a silent no-op.
func (a *ResumeAccount) UpdateExperienceMeta(ctx context.Context, expID int, teamSize, budgetUSD *int, domain string, isVolunteer bool) error {
	if !a.writable() {
		return ErrNoAccountScope
	}
	_, err := a.conn(ctx).Exec(ctx,
		`UPDATE resume_experiences SET team_size = $2, budget_usd = $3, domain = $4, is_volunteer = $5
		 WHERE id = $1 AND `+personOwnedBy6,
		expID, teamSize, budgetUSD, domain, isVolunteer, a.aid,
	)
	return err
}

// InsertProjectWithParent inserts a project linked to a parent experience.
// parent_experience_id keeps its ON DELETE SET NULL semantics; the person_id
// predicate proves the bound account owns the parent person.
func (a *ResumeAccount) InsertProjectWithParent(ctx context.Context, personID int, parentExpID *int, p ProjectRecord) (int, error) {
	if !a.writable() {
		return 0, ErrNoAccountScope
	}
	var id int
	err := a.conn(ctx).QueryRow(ctx,
		`INSERT INTO resume_projects (person_id, name, description, url, tech, highlights, parent_experience_id)
		 SELECT $1, $2, $3, $4, $5, $6, $7
		 WHERE EXISTS (SELECT 1 FROM resume_persons WHERE id = $1 AND account_id = $8)
		 RETURNING id`,
		personID, p.Name, p.Description, p.URL, p.Tech, p.Highlights, parentExpID, a.aid,
	).Scan(&id)
	return id, err
}

// MarkPersonEnriched sets the enriched_at timestamp on a person owned by the
// bound account; a foreign id is a silent no-op.
func (a *ResumeAccount) MarkPersonEnriched(ctx context.Context, personID int) error {
	if !a.writable() {
		return ErrNoAccountScope
	}
	_, err := a.conn(ctx).Exec(ctx,
		`UPDATE resume_persons SET enriched_at = now() WHERE id = $1 AND account_id = $2`, personID, a.aid)
	return err
}

// InsertSkillExtended inserts a skill with implicit/source tracking.
func (a *ResumeAccount) InsertSkillExtended(ctx context.Context, personID int, s SkillRecord) (int, error) {
	if !a.writable() {
		return 0, ErrNoAccountScope
	}
	var id int
	err := a.conn(ctx).QueryRow(ctx,
		`INSERT INTO resume_skills (person_id, name, category, level, is_implicit, source)
		 SELECT $1, $2, $3, $4, $5, $6
		 WHERE EXISTS (SELECT 1 FROM resume_persons WHERE id = $1 AND account_id = $7)
		 ON CONFLICT (person_id, name) DO UPDATE SET category = EXCLUDED.category, level = EXCLUDED.level, is_implicit = EXCLUDED.is_implicit, source = EXCLUDED.source
		 RETURNING id`,
		personID, s.Name, s.Category, s.Level, s.IsImplicit, s.Source, a.aid,
	).Scan(&id)
	return id, err
}

// InsertAchievementExtended inserts an achievement with parsed metric fields.
func (a *ResumeAccount) InsertAchievementExtended(ctx context.Context, personID int, ach AchievementRecord) (int, error) {
	if !a.writable() {
		return 0, ErrNoAccountScope
	}
	var id int
	err := a.conn(ctx).QueryRow(ctx,
		`INSERT INTO resume_achievements (person_id, text, metric, value, context, metric_numeric, metric_unit)
		 SELECT $1, $2, $3, $4, $5, $6, $7
		 WHERE EXISTS (SELECT 1 FROM resume_persons WHERE id = $1 AND account_id = $8)
		 RETURNING id`,
		personID, ach.Text, ach.Metric, ach.Value, ach.Context, ach.MetricNumeric, ach.MetricUnit, a.aid,
	).Scan(&id)
	return id, err
}
