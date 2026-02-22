package jobs

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema/*.sql
var schemaFS embed.FS

// ageSetup runs per-connection AGE initialization.
const ageSetup = `LOAD 'age'; SET search_path TO ag_catalog, "$user", public`

// Package-level singletons, set from main.go.
var (
	resumeDB *ResumeDB
	memDB    *MemDBClient
)

// SetResumeDB sets the package-level resume DB instance.
func SetResumeDB(db *ResumeDB) { resumeDB = db }

// GetResumeDB returns the package-level resume DB instance (may be nil).
func GetResumeDB() *ResumeDB { return resumeDB }

// SetMemDB sets the package-level MemDB client instance.
func SetMemDB(c *MemDBClient) { memDB = c }

// GetMemDB returns the package-level MemDB client instance (may be nil).
func GetMemDB() *MemDBClient { return memDB }

// ResumeDB holds the pgx connection pool for resume storage.
type ResumeDB struct {
	pool *pgxpool.Pool
}

// ConnectResumeDB creates a pgx pool and runs schema migrations.
func ConnectResumeDB(ctx context.Context, databaseURL string) (*ResumeDB, error) {
	if databaseURL == "" {
		return nil, errors.New("DATABASE_URL is required")
	}

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	config.MaxConns = 10
	config.MinConns = 1

	// Force search_path to public for all pool connections.
	// The memos role has ag_catalog first in search_path (for MemDB graph),
	// which causes CREATE TABLE / INSERT to resolve to ag_catalog instead of public.
	// AGE graph queries explicitly set their own search_path via ageSetup.
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET search_path TO public")
		return err
	}

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create pgx pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	db := &ResumeDB{pool: pool}
	if err := db.runMigrations(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	slog.Info("resume postgres connected", slog.String("addr", config.ConnConfig.Host))
	return db, nil
}

func (db *ResumeDB) Close() {
	db.pool.Close()
}

func (db *ResumeDB) runMigrations(ctx context.Context) error {
	entries, err := schemaFS.ReadDir("schema")
	if err != nil {
		return fmt.Errorf("read schema dir: %w", err)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	// Run migrations on a single dedicated connection to avoid search_path issues
	// across pooled connections. The memos role has ag_catalog first in search_path.
	conn, err := db.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Release()

	// Ensure we create tables in public schema
	if _, err := conn.Exec(ctx, "SET search_path TO public"); err != nil {
		return fmt.Errorf("set search_path: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		data, err := schemaFS.ReadFile("schema/" + entry.Name())
		if err != nil {
			return fmt.Errorf("read %s: %w", entry.Name(), err)
		}

		if _, err := conn.Exec(ctx, string(data)); err != nil {
			if strings.Contains(entry.Name(), "002") {
				slog.Warn("AGE migration failed (Apache AGE may not be installed)",
					slog.String("file", entry.Name()),
					slog.Any("error", err))
				// Reset search_path after AGE migration (it sets search_path to ag_catalog)
				_, _ = conn.Exec(ctx, "SET search_path TO public")
				continue
			}
			return fmt.Errorf("execute %s: %w", entry.Name(), err)
		}

		// 002_resume_graph.sql sets search_path to ag_catalog; reset it for subsequent migrations
		if strings.Contains(entry.Name(), "002") {
			_, _ = conn.Exec(ctx, "SET search_path TO public")
		}

		slog.Info("migration applied", slog.String("file", entry.Name()))
	}
	return nil
}

// --- Person CRUD ---

type PersonRecord struct {
	ID       int               `json:"id"`
	Name     string            `json:"name"`
	Email    string            `json:"email"`
	Phone    string            `json:"phone"`
	Location string            `json:"location"`
	Links    map[string]string `json:"links"`
	Summary  string            `json:"summary"`
}

func (db *ResumeDB) InsertPerson(ctx context.Context, p PersonRecord) (int, error) {
	linksJSON, _ := json.Marshal(p.Links)
	var id int
	err := db.pool.QueryRow(ctx,
		`INSERT INTO resume_persons (name, email, phone, location, links, summary)
		 VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		p.Name, p.Email, p.Phone, p.Location, linksJSON, p.Summary,
	).Scan(&id)
	return id, err
}

func (db *ResumeDB) ClearPerson(ctx context.Context, personID int) error {
	_, err := db.pool.Exec(ctx, `DELETE FROM resume_persons WHERE id = $1`, personID)
	return err
}

// ClearAllPersons deletes all resume data (single-user system, rebuild from scratch).
func (db *ResumeDB) ClearAllPersons(ctx context.Context) error {
	_, err := db.pool.Exec(ctx, `DELETE FROM resume_persons`)
	return err
}

// GetLatestPersonID returns the ID of the most recently created person, or 0 if none.
func (db *ResumeDB) GetLatestPersonID(ctx context.Context) int {
	var id int
	err := db.pool.QueryRow(ctx, `SELECT id FROM resume_persons ORDER BY id DESC LIMIT 1`).Scan(&id)
	if err != nil {
		return 0
	}
	return id
}

// GetPerson returns the person record for the given ID.
func (db *ResumeDB) GetPerson(ctx context.Context, personID int) (*PersonRecord, error) {
	var p PersonRecord
	var linksJSON []byte
	err := db.pool.QueryRow(ctx,
		`SELECT id, name, COALESCE(email,''), COALESCE(phone,''), COALESCE(location,''), COALESCE(links,'{}'), COALESCE(summary,'')
		 FROM resume_persons WHERE id = $1`, personID,
	).Scan(&p.ID, &p.Name, &p.Email, &p.Phone, &p.Location, &linksJSON, &p.Summary)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(linksJSON, &p.Links)
	return &p, nil
}

// GetPersonEnrichedAt returns the enriched_at timestamp as a string, or empty if not enriched.
func (db *ResumeDB) GetPersonEnrichedAt(ctx context.Context, personID int) string {
	var enrichedAt *string
	err := db.pool.QueryRow(ctx,
		`SELECT enriched_at::text FROM resume_persons WHERE id = $1`, personID,
	).Scan(&enrichedAt)
	if err != nil || enrichedAt == nil {
		return ""
	}
	return *enrichedAt
}

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

// --- AGE Graph Helpers ---

func (db *ResumeDB) UpsertGraphNode(ctx context.Context, label string, id int, props map[string]string) error {
	conn, err := db.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, ageSetup); err != nil {
		return fmt.Errorf("age setup: %w", err)
	}

	var setParts []string
	for k, v := range props {
		setParts = append(setParts, fmt.Sprintf("n.%s = '%s'", escapeCypher(k), escapeCypher(v)))
	}
	setClause := ""
	if len(setParts) > 0 {
		setClause = "SET " + strings.Join(setParts, ", ")
	}

	cypher := fmt.Sprintf(`
		SELECT * FROM ag_catalog.cypher('resume_graph', $$
			MERGE (n:%s {id: %d})
			%s
			RETURN n
		$$) AS (n ag_catalog.agtype)`,
		label, id, setClause,
	)
	if _, err := conn.Exec(ctx, cypher); err != nil {
		return fmt.Errorf("upsert node %s:%d: %w", label, id, err)
	}
	return nil
}

func (db *ResumeDB) UpsertGraphEdge(ctx context.Context, fromLabel string, fromID int, edgeLabel string, toLabel string, toID int) error {
	conn, err := db.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, ageSetup); err != nil {
		return fmt.Errorf("age setup: %w", err)
	}

	cypher := fmt.Sprintf(`
		SELECT * FROM ag_catalog.cypher('resume_graph', $$
			MATCH (a:%s {id: %d}), (b:%s {id: %d})
			MERGE (a)-[:%s]->(b)
		$$) AS (result ag_catalog.agtype)`,
		fromLabel, fromID, toLabel, toID, edgeLabel,
	)
	if _, err := conn.Exec(ctx, cypher); err != nil {
		return fmt.Errorf("upsert edge %s:%d->%s->%s:%d: %w", fromLabel, fromID, edgeLabel, toLabel, toID, err)
	}
	return nil
}

// ClearGraph removes all nodes and edges from the resume_graph.
func (db *ResumeDB) ClearGraph(ctx context.Context) error {
	conn, err := db.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, ageSetup); err != nil {
		return fmt.Errorf("age setup: %w", err)
	}

	cypher := `SELECT * FROM ag_catalog.cypher('resume_graph', $$
		MATCH (n) DETACH DELETE n
	$$) AS (result ag_catalog.agtype)`
	_, err = conn.Exec(ctx, cypher)
	return err
}

// QueryExperienceIDsBySkill finds experience IDs linked to a skill name via the graph.
func (db *ResumeDB) QueryExperienceIDsBySkill(ctx context.Context, skillName string) ([]int, error) {
	conn, err := db.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, ageSetup); err != nil {
		return nil, fmt.Errorf("age setup: %w", err)
	}

	cypher := fmt.Sprintf(`
		SELECT * FROM ag_catalog.cypher('resume_graph', $$
			MATCH (e:Exp)-[:USED_SKILL]->(s:Skill {name: '%s'})
			RETURN e.id
		$$) AS (id ag_catalog.agtype)`, escapeCypher(skillName))

	rows, err := conn.Query(ctx, cypher)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanAGEIntIDs(rows)
}

// QueryProjectIDsBySkill finds project IDs linked to a skill name via the graph.
func (db *ResumeDB) QueryProjectIDsBySkill(ctx context.Context, skillName string) ([]int, error) {
	conn, err := db.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, ageSetup); err != nil {
		return nil, fmt.Errorf("age setup: %w", err)
	}

	cypher := fmt.Sprintf(`
		SELECT * FROM ag_catalog.cypher('resume_graph', $$
			MATCH (p:Proj)-[:USED_SKILL]->(s:Skill {name: '%s'})
			RETURN p.id
		$$) AS (id ag_catalog.agtype)`, escapeCypher(skillName))

	rows, err := conn.Query(ctx, cypher)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanAGEIntIDs(rows)
}

// QueryAchievementIDsByExperience finds achievement IDs produced by an experience.
func (db *ResumeDB) QueryAchievementIDsByExperience(ctx context.Context, expID int) ([]int, error) {
	conn, err := db.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, ageSetup); err != nil {
		return nil, fmt.Errorf("age setup: %w", err)
	}

	cypher := fmt.Sprintf(`
		SELECT * FROM ag_catalog.cypher('resume_graph', $$
			MATCH (e:Exp {id: %d})-[:PRODUCED]->(a:Achv)
			RETURN a.id
		$$) AS (id ag_catalog.agtype)`, expID)

	rows, err := conn.Query(ctx, cypher)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanAGEIntIDs(rows)
}

// --- Extended Graph Queries ---

// QueryExperienceIDsByDomain finds experience IDs linked to a domain via the graph.
func (db *ResumeDB) QueryExperienceIDsByDomain(ctx context.Context, domain string) ([]int, error) {
	conn, err := db.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, ageSetup); err != nil {
		return nil, fmt.Errorf("age setup: %w", err)
	}

	cypher := fmt.Sprintf(`
		SELECT * FROM ag_catalog.cypher('resume_graph', $$
			MATCH (e:Exp)-[:IN_DOMAIN]->(d:Domain {name: '%s'})
			RETURN e.id
		$$) AS (id ag_catalog.agtype)`, escapeCypher(domain))

	rows, err := conn.Query(ctx, cypher)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAGEIntIDs(rows)
}

// QueryImpliedSkillIDs returns skill IDs reachable via 1-hop IMPLIES_SKILL from skillID.
func (db *ResumeDB) QueryImpliedSkillIDs(ctx context.Context, skillID int) ([]int, error) {
	conn, err := db.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, ageSetup); err != nil {
		return nil, fmt.Errorf("age setup: %w", err)
	}

	cypher := fmt.Sprintf(`
		SELECT * FROM ag_catalog.cypher('resume_graph', $$
			MATCH (s:Skill {id: %d})-[:IMPLIES_SKILL]->(t:Skill)
			RETURN t.id
		$$) AS (id ag_catalog.agtype)`, skillID)

	rows, err := conn.Query(ctx, cypher)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAGEIntIDs(rows)
}

// QuerySubProjectIDs returns project IDs linked to an experience via PART_OF.
func (db *ResumeDB) QuerySubProjectIDs(ctx context.Context, expID int) ([]int, error) {
	conn, err := db.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, ageSetup); err != nil {
		return nil, fmt.Errorf("age setup: %w", err)
	}

	cypher := fmt.Sprintf(`
		SELECT * FROM ag_catalog.cypher('resume_graph', $$
			MATCH (p:Proj)-[:PART_OF]->(e:Exp {id: %d})
			RETURN p.id
		$$) AS (id ag_catalog.agtype)`, expID)

	rows, err := conn.Query(ctx, cypher)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAGEIntIDs(rows)
}

// TrajectoryEdge represents a career evolution edge.
type TrajectoryEdge struct {
	FromExpID int    `json:"from_exp_id"`
	ToExpID   int    `json:"to_exp_id"`
	FromTitle string `json:"from_title"`
	ToTitle   string `json:"to_title"`
}

// QueryCareerTrajectory returns EVOLVED_TO edges for a person's career graph.
func (db *ResumeDB) QueryCareerTrajectory(ctx context.Context, personID int) ([]TrajectoryEdge, error) {
	conn, err := db.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, ageSetup); err != nil {
		return nil, fmt.Errorf("age setup: %w", err)
	}

	// Get all EVOLVED_TO edges between Exp nodes
	cypher := `
		SELECT * FROM ag_catalog.cypher('resume_graph', $$
			MATCH (a:Exp)-[:EVOLVED_TO]->(b:Exp)
			RETURN a.id, b.id, a.title, b.title
		$$) AS (from_id ag_catalog.agtype, to_id ag_catalog.agtype, from_title ag_catalog.agtype, to_title ag_catalog.agtype)`

	rows, err := conn.Query(ctx, cypher)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var edges []TrajectoryEdge
	for rows.Next() {
		var fID, tID, fTitle, tTitle string
		if err := rows.Scan(&fID, &tID, &fTitle, &tTitle); err != nil {
			continue
		}
		var e TrajectoryEdge
		_, _ = fmt.Sscanf(strings.TrimSpace(fID), "%d", &e.FromExpID)
		_, _ = fmt.Sscanf(strings.TrimSpace(tID), "%d", &e.ToExpID)
		e.FromTitle = strings.Trim(strings.TrimSpace(fTitle), `"`)
		e.ToTitle = strings.Trim(strings.TrimSpace(tTitle), `"`)
		edges = append(edges, e)
	}
	return edges, rows.Err()
}

// QuerySkillIDByName returns the skill ID for a given name, or 0 if not found.
func (db *ResumeDB) QuerySkillIDByName(ctx context.Context, personID int, skillName string) int {
	var id int
	err := db.pool.QueryRow(ctx,
		`SELECT id FROM resume_skills WHERE person_id = $1 AND LOWER(name) = LOWER($2)`,
		personID, skillName,
	).Scan(&id)
	if err != nil {
		return 0
	}
	return id
}

// CountGraphNodes returns the total number of nodes in the resume graph.
func (db *ResumeDB) CountGraphNodes(ctx context.Context) (int, error) {
	conn, err := db.pool.Acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, ageSetup); err != nil {
		return 0, err
	}

	cypher := `SELECT * FROM ag_catalog.cypher('resume_graph', $$
		MATCH (n) RETURN count(n)
	$$) AS (count ag_catalog.agtype)`

	var raw string
	if err := conn.QueryRow(ctx, cypher).Scan(&raw); err != nil {
		return 0, err
	}
	// AGE returns agtype: e.g. "5" or "5::integer"
	var count int
	_, _ = fmt.Sscanf(strings.TrimSpace(raw), "%d", &count)
	return count, nil
}

// CountGraphEdges returns the total number of edges in the resume graph.
func (db *ResumeDB) CountGraphEdges(ctx context.Context) (int, error) {
	conn, err := db.pool.Acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, ageSetup); err != nil {
		return 0, err
	}

	cypher := `SELECT * FROM ag_catalog.cypher('resume_graph', $$
		MATCH ()-[r]->() RETURN count(r)
	$$) AS (count ag_catalog.agtype)`

	var raw string
	if err := conn.QueryRow(ctx, cypher).Scan(&raw); err != nil {
		return 0, err
	}
	var count int
	_, _ = fmt.Sscanf(strings.TrimSpace(raw), "%d", &count)
	return count, nil
}

// scanAGEIntIDs scans agtype integer results into []int.
func scanAGEIntIDs(rows pgx.Rows) ([]int, error) {
	var ids []int
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			continue
		}
		var id int
		if _, err := fmt.Sscanf(strings.TrimSpace(raw), "%d", &id); err == nil {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}

// escapeCypher escapes a string for safe use in a single-quoted Cypher literal.
func escapeCypher(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "`", "\\`")
	s = strings.ReplaceAll(s, "\x00", "")
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, "\r", `\r`)
	s = strings.ReplaceAll(s, "\t", `\t`)
	return s
}
