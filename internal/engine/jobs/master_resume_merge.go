package jobs

// master_resume_merge.go — the merge variant of the master-resume build.
// The write path is identical to BuildMasterResume (atomic replace under the
// advisory lock); what differs is the first LLM prompt, which is seeded with
// the current profile so the model merges instead of re-deriving.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// masterResumeMergeRules prefixes the parse prompt in merge mode. The existing
// profile is the authoritative baseline; the resume text at the end of the
// combined prompt is the NEW document to fold in.
const masterResumeMergeRules = `You are updating an existing structured resume profile using a new resume document.
The EXISTING PROFILE JSON below is the authoritative baseline — it may contain manual edits the user made after a previous import; treat those edits as intentional.

Merge rules:
- Preserve every entity in the existing profile unless the new resume clearly contradicts it.
- Add every entity present in the new resume but missing from the profile.
- When the new resume refines an existing entity (new highlights, updated dates, a richer description), merge the improvement into that entity rather than duplicating it.
- The new document may be partial — do not drop existing entities merely because it omits them.

The instructions below describe the output schema. Produce the MERGED profile in that schema.`

// mergeSnapshotMaxRunes bounds the profile baseline embedded in the merge
// prompt. Exceeding it REFUSES the merge (fail-closed): a truncated baseline
// would silently destroy tail entities on write — the LLM can only preserve
// what it sees. A var so tests can shrink it.
var mergeSnapshotMaxRunes = 48000

// mergePrompt builds the merge-seeded parse prompt plus the baseline JSON it
// embedded. Both return empty when no profile exists — merge then degrades to
// a plain build. The baseline is re-verified inside the write tx before the
// profile is cleared, closing the lost-update window between the LLM call and
// the commit. A guard error refuses rather than silently building without the
// baseline (the model would not know what to preserve).
func mergePrompt(ctx context.Context, rdb *ResumeAccount, resumeTrunc string) (string, string, error) {
	exists, personID, err := rdb.guardLatestPersonID(ctx)
	if err != nil {
		return "", "", fmt.Errorf("master_resume_merge: profile probe failed (refusing): %w", err)
	}
	if !exists {
		return "", "", nil
	}
	snap, err := profileSnapshotJSON(ctx, rdb, personID)
	if err != nil {
		return "", "", fmt.Errorf("master_resume_merge: profile snapshot: %w", err)
	}
	if n := utf8.RuneCountInString(snap); n > mergeSnapshotMaxRunes {
		return "", "", fmt.Errorf("master_resume_merge: existing profile too large to merge (%d runes, cap %d) — use Rebuild instead",
			n, mergeSnapshotMaxRunes)
	}
	return fmt.Sprintf("%s\n\nEXISTING PROFILE JSON:\n%s\n\n%s",
		masterResumeMergeRules, snap,
		fmt.Sprintf(masterResumeParsePrompt, resumeTrunc)), snap, nil
}

// profileSnapshotJSON serializes the full current profile in the same shape
// the parse schema re-emits — the model can pass the baseline through
// unchanged where the new document adds nothing. Fail-closed: a missing
// section would let the model drop those entities, so any load error refuses
// the merge.
func profileSnapshotJSON(ctx context.Context, rdb *ResumeAccount, personID int) (string, error) {
	person, err := rdb.GetPerson(ctx, personID)
	if err != nil {
		return "", fmt.Errorf("person: %w", err)
	}
	exps, err := rdb.GetAllExperiences(ctx, personID)
	if err != nil {
		return "", fmt.Errorf("experiences: %w", err)
	}
	projs, err := rdb.GetAllProjects(ctx, personID)
	if err != nil {
		return "", fmt.Errorf("projects: %w", err)
	}
	skills, err := rdb.GetAllSkills(ctx, personID)
	if err != nil {
		return "", fmt.Errorf("skills: %w", err)
	}
	achvs, err := rdb.GetAllAchievements(ctx, personID)
	if err != nil {
		return "", fmt.Errorf("achievements: %w", err)
	}
	eds, err := rdb.GetAllEducations(ctx, personID)
	if err != nil {
		return "", fmt.Errorf("educations: %w", err)
	}
	certs, err := rdb.GetAllCertifications(ctx, personID)
	if err != nil {
		return "", fmt.Errorf("certifications: %w", err)
	}
	doms, err := rdb.GetAllDomains(ctx, personID)
	if err != nil {
		return "", fmt.Errorf("domains: %w", err)
	}
	meths, err := rdb.GetAllMethodologies(ctx, personID)
	if err != nil {
		return "", fmt.Errorf("methodologies: %w", err)
	}

	snap := mergeSnapshot{
		Person: mergeSnapshotPerson{
			Name:     person.Name,
			Email:    person.Email,
			Phone:    person.Phone,
			Location: person.Location,
			Links:    person.Links,
			Summary:  person.Summary,
		},
	}
	// Sub-projects nest under their parent experience (the output schema
	// shape); a stale parent_experience_id pointing nowhere falls back to a
	// top-level project rather than vanishing.
	subByParent := map[int][]mergeSnapshotProject{}
	for _, p := range projs {
		if p.ParentExperienceID != nil {
			subByParent[*p.ParentExperienceID] = append(subByParent[*p.ParentExperienceID], snapshotProject(p))
		}
	}
	for _, e := range exps {
		se := mergeSnapshotExperience{
			Title: e.Title, Company: e.Company, Location: e.Location,
			StartDate: e.StartDate, EndDate: e.EndDate, Description: e.Description,
			Highlights: e.Highlights, Domain: e.Domain,
			TeamSize: e.TeamSize, BudgetUSD: e.BudgetUSD, IsVolunteer: e.IsVolunteer,
			SubProjects: subByParent[e.ID],
		}
		delete(subByParent, e.ID)
		snap.Experiences = append(snap.Experiences, se)
	}
	for _, p := range projs {
		if p.ParentExperienceID == nil {
			snap.Projects = append(snap.Projects, snapshotProject(p))
		}
	}
	// Orphans: sort by parent id — map iteration order is random, and the
	// in-tx drift check byte-compares two snapshots of identical DB state.
	orphanKeys := make([]int, 0, len(subByParent))
	for pid := range subByParent {
		orphanKeys = append(orphanKeys, pid)
	}
	sort.Ints(orphanKeys)
	for _, pid := range orphanKeys {
		snap.Projects = append(snap.Projects, subByParent[pid]...)
	}
	for _, s := range skills {
		snap.Skills = append(snap.Skills, mergeSnapshotSkill{
			Name: s.Name, Category: s.Category, Level: s.Level,
			IsImplicit: s.IsImplicit, Source: s.Source,
		})
	}
	for _, a := range achvs {
		snap.Achievements = append(snap.Achievements, mergeSnapshotAchievement{
			Text: a.Text, Metric: a.Metric, Value: a.Value, Context: a.Context,
			MetricNumeric: a.MetricNumeric, MetricUnit: a.MetricUnit,
		})
	}
	for _, e := range eds {
		snap.Educations = append(snap.Educations, mergeSnapshotEducation{
			School: e.School, Degree: e.Degree, Field: e.Field,
			StartDate: e.StartDate, EndDate: e.EndDate, GPA: e.GPA, Highlights: e.Highlights,
		})
	}
	for _, c := range certs {
		snap.Certifications = append(snap.Certifications, mergeSnapshotCertification{
			Name: c.Name, Issuer: c.Issuer, Year: c.Year, URL: c.URL,
		})
	}
	for _, d := range doms {
		snap.Domains = append(snap.Domains, d.Name)
	}
	for _, m := range meths {
		snap.Methodologies = append(snap.Methodologies, mergeSnapshotMethodology{
			Name: m.Name, Description: m.Description,
		})
	}
	b, err := json.Marshal(snap)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// --- snapshot entity shapes: parsedResume output schema field-for-field ---

type mergeSnapshot struct {
	Person         mergeSnapshotPerson          `json:"person"`
	Experiences    []mergeSnapshotExperience    `json:"experiences"`
	Educations     []mergeSnapshotEducation     `json:"educations"`
	Skills         []mergeSnapshotSkill         `json:"skills"`
	Projects       []mergeSnapshotProject       `json:"projects"`
	Achievements   []mergeSnapshotAchievement   `json:"achievements"`
	Certifications []mergeSnapshotCertification `json:"certifications"`
	Domains        []string                     `json:"domains"`
	Methodologies  []mergeSnapshotMethodology   `json:"methodologies"`
}

type mergeSnapshotPerson struct {
	Name     string            `json:"name"`
	Email    string            `json:"email"`
	Phone    string            `json:"phone"`
	Location string            `json:"location"`
	Links    map[string]string `json:"links"`
	Summary  string            `json:"summary"`
}

// Note: experiences[].skills is absent — the DB stores no per-experience
// skill list (exp skills become USED_SKILL graph edges at write time); the
// LLM re-derives them from the description on merge.
type mergeSnapshotExperience struct {
	Title       string                 `json:"title"`
	Company     string                 `json:"company"`
	Location    string                 `json:"location"`
	StartDate   string                 `json:"start_date"`
	EndDate     string                 `json:"end_date"`
	Description string                 `json:"description"`
	Highlights  []string               `json:"highlights"`
	Domain      string                 `json:"domain,omitempty"`
	TeamSize    *int                   `json:"team_size,omitempty"`
	BudgetUSD   *int                   `json:"budget_usd,omitempty"`
	IsVolunteer bool                   `json:"is_volunteer,omitempty"`
	SubProjects []mergeSnapshotProject `json:"sub_projects,omitempty"`
}

type mergeSnapshotSkill struct {
	Name       string `json:"name"`
	Category   string `json:"category"`
	Level      string `json:"level"`
	IsImplicit bool   `json:"is_implicit,omitempty"`
	Source     string `json:"source,omitempty"`
}

type mergeSnapshotProject struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	URL         string   `json:"url"`
	Tech        []string `json:"tech"`
	Highlights  []string `json:"highlights"`
}

func snapshotProject(p ProjectRecord) mergeSnapshotProject {
	return mergeSnapshotProject{
		Name: p.Name, Description: p.Description, URL: p.URL,
		Tech: p.Tech, Highlights: p.Highlights,
	}
}

type mergeSnapshotAchievement struct {
	Text          string   `json:"text"`
	Metric        string   `json:"metric"`
	Value         string   `json:"value"`
	Context       string   `json:"context"`
	MetricNumeric *float64 `json:"metric_numeric,omitempty"`
	MetricUnit    string   `json:"metric_unit,omitempty"`
}

type mergeSnapshotEducation struct {
	School     string   `json:"school"`
	Degree     string   `json:"degree"`
	Field      string   `json:"field"`
	StartDate  string   `json:"start_date"`
	EndDate    string   `json:"end_date"`
	GPA        string   `json:"gpa"`
	Highlights []string `json:"highlights"`
}

type mergeSnapshotCertification struct {
	Name   string `json:"name"`
	Issuer string `json:"issuer"`
	Year   string `json:"year"`
	URL    string `json:"url"`
}

type mergeSnapshotMethodology struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// --- preserved state: manual edits the output schema cannot round-trip ---

// preservedProfileState captures the manually-edited state the LLM output
// schema cannot re-emit: person headline/hourly_rate (edit-UI columns) and
// the whole upwork_* set. The write phase captures it inside the transaction
// BEFORE ClearAllPersons and restores it on the new person row — mechanical,
// never through the LLM, so a merge cannot silently destroy it and a rebuild
// only loses what its consent actually promised.
type preservedProfileState struct {
	Headline        string
	HourlyRateCents int64
	Upwork          *UpworkProfile
	UpworkSkills    []UpworkSkillRecord
	UpworkCatalog   []UpworkCatalogItem
}

func capturePreservedState(ctx context.Context, rdb *ResumeAccount, personID int) (*preservedProfileState, error) {
	st := &preservedProfileState{}
	err := rdb.conn(ctx).QueryRow(ctx,
		`SELECT COALESCE(headline,''), COALESCE(hourly_rate,0)
		 FROM resume_persons WHERE id = $1 AND account_id = $2`, personID, rdb.aid).
		Scan(&st.Headline, &st.HourlyRateCents)
	if err != nil {
		return nil, fmt.Errorf("capture person fields: %w", err)
	}
	up := &UpworkProfile{}
	err = rdb.conn(ctx).QueryRow(ctx, getUpworkProfileSQL, personID, rdb.aid).
		Scan(&up.Title, &up.Overview, &up.HourlyRate, &up.Categories, &up.Availability)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return nil, fmt.Errorf("capture upwork profile: %w", err)
	default:
		st.Upwork = up
	}
	rows, err := rdb.conn(ctx).Query(ctx, getUpworkSkillsSQL, personID, rdb.aid)
	if err != nil {
		return nil, fmt.Errorf("capture upwork skills: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var s UpworkSkillRecord
		if err := rows.Scan(&s.ID, &s.Name, &s.Position); err != nil {
			return nil, fmt.Errorf("capture upwork skills: %w", err)
		}
		st.UpworkSkills = append(st.UpworkSkills, s)
	}
	catRows, err := rdb.conn(ctx).Query(ctx, getUpworkCatalogItemsSQL, personID, rdb.aid)
	if err != nil {
		return nil, fmt.Errorf("capture upwork catalog: %w", err)
	}
	defer catRows.Close()
	for catRows.Next() {
		var c UpworkCatalogItem
		if err := catRows.Scan(&c.ID, &c.Title, &c.Description, &c.Position); err != nil {
			return nil, fmt.Errorf("capture upwork catalog: %w", err)
		}
		st.UpworkCatalog = append(st.UpworkCatalog, c)
	}
	return st, nil
}

func restorePreservedState(ctx context.Context, rdb *ResumeAccount, personID int, st *preservedProfileState) error {
	if _, err := rdb.conn(ctx).Exec(ctx,
		`UPDATE resume_persons SET headline = $2, hourly_rate = $3 WHERE id = $1 AND account_id = $4`,
		personID, st.Headline, st.HourlyRateCents, rdb.aid); err != nil {
		return fmt.Errorf("restore person fields: %w", err)
	}
	if st.Upwork != nil {
		if _, err := rdb.conn(ctx).Exec(ctx,
			`INSERT INTO upwork_profile (person_id, title, overview, hourly_rate, categories, availability)
			 SELECT $1, $2, $3, $4, $5, $6
			 WHERE EXISTS (SELECT 1 FROM resume_persons WHERE id = $1 AND account_id = $7)`,
			personID, st.Upwork.Title, st.Upwork.Overview, st.Upwork.HourlyRate,
			st.Upwork.Categories, st.Upwork.Availability, rdb.aid); err != nil {
			return fmt.Errorf("restore upwork profile: %w", err)
		}
	}
	for _, s := range st.UpworkSkills {
		if _, err := rdb.conn(ctx).Exec(ctx,
			`INSERT INTO upwork_skills (person_id, name, position)
			 SELECT $1, $2, $3
			 WHERE EXISTS (SELECT 1 FROM resume_persons WHERE id = $1 AND account_id = $4)`,
			personID, s.Name, s.Position, rdb.aid); err != nil {
			return fmt.Errorf("restore upwork skill: %w", err)
		}
	}
	for _, c := range st.UpworkCatalog {
		if _, err := rdb.conn(ctx).Exec(ctx,
			`INSERT INTO upwork_catalog_items (person_id, title, description, position)
			 SELECT $1, $2, $3, $4
			 WHERE EXISTS (SELECT 1 FROM resume_persons WHERE id = $1 AND account_id = $5)`,
			personID, c.Title, c.Description, c.Position, rdb.aid); err != nil {
			return fmt.Errorf("restore upwork catalog item: %w", err)
		}
	}
	return nil
}
