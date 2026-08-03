package adminui

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"

	"github.com/anatolykoptev/go-kit/admintable"
	"github.com/anatolykoptev/go-panel/resource"
	"github.com/anatolykoptev/go-panel/tenant"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/jackc/pgx/v5/pgxpool"
)

// grpResume is the sidebar group label for resume-related resources.
// resumeEditURL is the redirect target after resume entity mutations.
const (
	grpResume     = "Resume"
	resumeEditURL = "/admin/resume/edit"
)

// experiencesSpec is the admintable spec for the experiences list page.
var experiencesSpec = admintable.Spec{
	Columns: []admintable.Column{
		{Key: "title", Label: "Title", Sortable: true, SQLExpr: "e.title"},
		{Key: "company", Label: "Company", Sortable: true, SQLExpr: "e.company"},
		{Key: "location", Label: "Location", Sortable: false, SQLExpr: "e.location"},
		{Key: "start_date", Label: "Start", Sortable: true, SQLExpr: "e.start_date", Width: "6rem"},
		{Key: "end_date", Label: "End", Sortable: true, SQLExpr: "e.end_date", Width: "6rem"},
	},
	DefaultKey: "start_date",
	DefaultDir: admintable.Desc,
}

// experiencesResource builds a go-panel Resource for resume experiences with
// full CRUD via Writer (create, edit, delete).
func experiencesResource(pool *pgxpool.Pool) resource.Resource {
	return resource.Resource{
		Name:   "experiences",
		Title:  "Experiences",
		Icon:   "💼",
		Group:  grpResume,
		Sort:   experiencesSpec,
		Filter: admintable.FilterSpec{},
		Lister: experiencesLister(pool),
		FetchRow: func(ctx context.Context, id string) (map[string]string, error) {
			expID, err := strconv.Atoi(id)
			if err != nil {
				return nil, resource.ErrDetailNotFound
			}
			db := jobs.GetResumeDB()
			if db == nil {
				return nil, resource.ErrDetailNotFound
			}
			e, err := db.GetExperienceByID(ctx, expID)
			if err != nil {
				return nil, resource.ErrDetailNotFound
			}
			return experienceToMap(e), nil
		},
		Writer: &resource.Writer{
			Form: resource.FormSpec{
				Fields: []resource.Field{
					{Key: "title", Label: "Title", Kind: resource.FieldText, Required: true},
					{Key: "company", Label: "Company", Kind: resource.FieldText, Required: true},
					{Key: "location", Label: "Location", Kind: resource.FieldText},
					{Key: "start_date", Label: "Start Date", Kind: resource.FieldText, Help: "YYYY-MM-DD or text like '2023'"},
					{Key: "end_date", Label: "End Date", Kind: resource.FieldText, Help: "YYYY-MM-DD, 'Present', or text"},
					{Key: "description", Label: "Description", Kind: resource.FieldTextarea},
					{Key: "highlights", Label: "Highlights", Kind: resource.FieldJSON, Help: "JSON array of strings, e.g. [\"Led team of 5\",\"Shipped X\"]"},
				},
			},
			Load: func(ctx context.Context, _ tenant.Tenant, id string) (map[string]string, error) {
				expID, err := strconv.Atoi(id)
				if err != nil {
					return nil, resource.ErrDetailNotFound
				}
				db := jobs.GetResumeDB()
				if db == nil {
					return nil, resource.ErrDetailNotFound
				}
				e, err := db.GetExperienceByID(ctx, expID)
				if err != nil {
					return nil, resource.ErrDetailNotFound
				}
				return experienceToMap(e), nil
			},
			Save: func(ctx context.Context, _ tenant.Tenant, id string, values map[string]string) error {
				db := jobs.GetResumeDB()
				if db == nil {
					return resource.NewSaveError("title", "resume database not configured")
				}
				highlights := parseHighlights(values["highlights"])
				e := jobs.ExperienceRecord{
					Title:       values["title"],
					Company:     values["company"],
					Location:    values["location"],
					StartDate:   values["start_date"],
					EndDate:     values["end_date"],
					Description: values["description"],
					Highlights:  highlights,
				}
				if id == "" {
					personID := db.GetLatestPersonID(ctx)
					if personID == 0 {
						return resource.NewSaveError("title", "no resume person found — run master_resume_build first")
					}
					_, err := db.InsertExperience(ctx, personID, e)
					return err
				}
				expID, err := strconv.Atoi(id)
				if err != nil {
					return resource.NewSaveError("title", "invalid experience ID")
				}
				return db.UpdateExperience(ctx, expID, e)
			},
			Delete: func(ctx context.Context, _ tenant.Tenant, id string) error {
				expID, err := strconv.Atoi(id)
				if err != nil {
					return resource.NewSaveError("title", "invalid experience ID")
				}
				db := jobs.GetResumeDB()
				if db == nil {
					return resource.NewSaveError("title", "resume database not configured")
				}
				return db.DeleteExperience(ctx, expID)
			},
			PresetValues: func(ctx context.Context, _ tenant.Tenant) (map[string]string, error) {
				// No preset values needed — person_id is resolved inside Save from
				// the latest person record. This hook is here for future use if
				// multi-person support is added.
				return nil, nil
			},
			AfterSave: func(ctx context.Context, _ string, err error) {
				if err != nil {
					return
				}
				personID := getLatestPersonIDSafe(ctx)
				if personID > 0 {
					syncProfileVectorsBestEffortCtx(ctx, personID)
				}
			},
			AfterDelete: func(ctx context.Context, id string, err error) {
				if err != nil {
					return
				}
				personID := getLatestPersonIDSafe(ctx)
				if personID > 0 {
					syncProfileVectorsBestEffortCtx(ctx, personID)
				}
			},
			RedirectAfterSave: func(_ context.Context, _ string) string {
				return resumeEditURL
			},
			RedirectAfterDelete: func(_ context.Context, _ string) string {
				return resumeEditURL
			},
		},
	}
}

// experiencesLister returns a Lister closure for the experiences resource.
func experiencesLister(pool *pgxpool.Pool) func(context.Context, resource.ListQuery) ([]resource.Row, int, error) {
	return func(ctx context.Context, q resource.ListQuery) ([]resource.Row, int, error) {
		db := jobs.GetResumeDB()
		if db == nil {
			return nil, 0, nil
		}
		personID := db.GetLatestPersonID(ctx)
		if personID == 0 {
			return nil, 0, nil
		}
		exps, err := db.GetAllExperiences(ctx, personID)
		if err != nil {
			slog.Error("experiencesLister: GetAllExperiences", "err", err)
			return nil, 0, err
		}
		rows := make([]resource.Row, 0, len(exps))
		for _, e := range exps {
			rows = append(rows, resource.Row{
				ID: strconv.Itoa(e.ID),
				Cells: []resource.Cell{
					{Value: e.Title},
					{Value: e.Company},
					{Value: e.Location},
					{Value: e.StartDate},
					{Value: e.EndDate},
				},
				Href: "/admin/experiences/" + strconv.Itoa(e.ID),
			})
		}
		return rows, len(rows), nil
	}
}

// experienceToMap converts an ExperienceRecord to a map for form values.
func experienceToMap(e jobs.ExperienceRecord) map[string]string {
	highlightsJSON := "[]"
	if len(e.Highlights) > 0 {
		if b, err := json.Marshal(e.Highlights); err == nil {
			highlightsJSON = string(b)
		}
	}
	return map[string]string{
		"title":       e.Title,
		"company":     e.Company,
		"location":    e.Location,
		"start_date":  e.StartDate,
		"end_date":    e.EndDate,
		"description": e.Description,
		"highlights":  highlightsJSON,
	}
}

// parseHighlights parses a JSON array string into a []string.
// Returns nil for empty/invalid input.
func parseHighlights(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" || s == "[]" {
		return nil
	}
	var result []string
	if err := json.Unmarshal([]byte(s), &result); err != nil {
		return nil
	}
	return result
}

// getLatestPersonIDSafe returns the latest person ID without panicking.
func getLatestPersonIDSafe(ctx context.Context) int {
	db := jobs.GetResumeDB()
	if db == nil {
		return 0
	}
	return db.GetLatestPersonID(ctx)
}

// syncProfileVectorsBestEffortCtx is the context-based version of
// syncProfileVectorsBestEffort for use in Writer hooks.
func syncProfileVectorsBestEffortCtx(ctx context.Context, personID int) {
	if err := jobs.SyncProfileVectors(ctx, personID); err != nil {
		slog.Warn("resume writer: profile vector sync failed (mutation already persisted)",
			slog.Int("person_id", personID), slog.Any("err", err))
	}
}

// --- Skills resource ---

// skillsSpec is the admintable spec for the skills list page.
var skillsSpec = admintable.Spec{
	Columns: []admintable.Column{
		{Key: "name", Label: "Skill", Sortable: true, SQLExpr: "s.name"},
		{Key: "category", Label: "Category", Sortable: true, SQLExpr: "s.category"},
		{Key: "level", Label: "Level", Sortable: true, SQLExpr: "s.level", Width: "8rem"},
	},
	DefaultKey: "name",
	DefaultDir: admintable.Asc,
}

// skillLevelOptions is the closed set of valid skill levels.
var skillLevelOptions = []resource.Option{
	{Value: "beginner", Label: "Beginner"},
	{Value: "intermediate", Label: "Intermediate"},
	{Value: "advanced", Label: "Advanced"},
	{Value: "expert", Label: "Expert"},
}

// skillsResource builds a go-panel Resource for resume skills with full CRUD.
func skillsResource(pool *pgxpool.Pool) resource.Resource {
	return resource.Resource{
		Name:   "skills",
		Title:  "Skills",
		Icon:   "🛠",
		Group:  grpResume,
		Sort:   skillsSpec,
		Filter: admintable.FilterSpec{},
		Lister: skillsLister(pool),
		FetchRow: func(ctx context.Context, id string) (map[string]string, error) {
			skillID, err := strconv.Atoi(id)
			if err != nil {
				return nil, resource.ErrDetailNotFound
			}
			db := jobs.GetResumeDB()
			if db == nil {
				return nil, resource.ErrDetailNotFound
			}
			s, err := db.GetSkillByID(ctx, skillID)
			if err != nil {
				return nil, resource.ErrDetailNotFound
			}
			return map[string]string{
				"name":     s.Name,
				"category": s.Category,
				"level":    s.Level,
			}, nil
		},
		Writer: &resource.Writer{
			Form: resource.FormSpec{
				Fields: []resource.Field{
					{Key: "name", Label: "Skill Name", Kind: resource.FieldText, Required: true},
					{Key: "category", Label: "Category", Kind: resource.FieldText, Help: "e.g. Backend, Frontend, DevOps, Security"},
					{Key: "level", Label: "Level", Kind: resource.FieldSelect, Options: skillLevelOptions},
				},
			},
			Load: func(ctx context.Context, _ tenant.Tenant, id string) (map[string]string, error) {
				skillID, err := strconv.Atoi(id)
				if err != nil {
					return nil, resource.ErrDetailNotFound
				}
				db := jobs.GetResumeDB()
				if db == nil {
					return nil, resource.ErrDetailNotFound
				}
				s, err := db.GetSkillByID(ctx, skillID)
				if err != nil {
					return nil, resource.ErrDetailNotFound
				}
				return map[string]string{
					"name":     s.Name,
					"category": s.Category,
					"level":    s.Level,
				}, nil
			},
			Save: func(ctx context.Context, _ tenant.Tenant, id string, values map[string]string) error {
				db := jobs.GetResumeDB()
				if db == nil {
					return resource.NewSaveError("name", "resume database not configured")
				}
				level := values["level"]
				if level == "" {
					level = "intermediate"
				}
				if !jobs.IsValidSkillLevel(level) {
					return resource.NewSaveError("level", "invalid skill level")
				}
				s := jobs.SkillRecord{
					Name:     values["name"],
					Category: values["category"],
					Level:    level,
				}
				if id == "" {
					personID := db.GetLatestPersonID(ctx)
					if personID == 0 {
						return resource.NewSaveError("name", "no resume person found — run master_resume_build first")
					}
					_, err := db.InsertSkill(ctx, personID, s)
					return err
				}
				skillID, err := strconv.Atoi(id)
				if err != nil {
					return resource.NewSaveError("name", "invalid skill ID")
				}
				return db.UpdateSkill(ctx, skillID, s)
			},
			Delete: func(ctx context.Context, _ tenant.Tenant, id string) error {
				skillID, err := strconv.Atoi(id)
				if err != nil {
					return resource.NewSaveError("name", "invalid skill ID")
				}
				db := jobs.GetResumeDB()
				if db == nil {
					return resource.NewSaveError("name", "resume database not configured")
				}
				return db.DeleteSkill(ctx, skillID)
			},
			RedirectAfterSave:   func(_ context.Context, _ string) string { return resumeEditURL },
			RedirectAfterDelete: func(_ context.Context, _ string) string { return resumeEditURL },
		},
	}
}

// skillsLister returns a Lister closure for the skills resource.
//nolint:dupl // structurally identical to other resume listers
func skillsLister(pool *pgxpool.Pool) func(context.Context, resource.ListQuery) ([]resource.Row, int, error) {
	return func(ctx context.Context, q resource.ListQuery) ([]resource.Row, int, error) {
		db := jobs.GetResumeDB()
		if db == nil {
			return nil, 0, nil
		}
		personID := db.GetLatestPersonID(ctx)
		if personID == 0 {
			return nil, 0, nil
		}
		skills, err := db.GetAllSkills(ctx, personID)
		if err != nil {
			slog.Error("skillsLister: GetAllSkills", "err", err)
			return nil, 0, err
		}
		rows := make([]resource.Row, 0, len(skills))
		for _, s := range skills {
			rows = append(rows, resource.Row{
				ID: strconv.Itoa(s.ID),
				Cells: []resource.Cell{
					{Value: s.Name},
					{Value: s.Category},
					{Value: s.Level},
				},
				Href: "/admin/skills/" + strconv.Itoa(s.ID),
			})
		}
		return rows, len(rows), nil
	}
}

// --- Achievements resource ---

var achievementsSpec = admintable.Spec{
	Columns: []admintable.Column{
		{Key: "text", Label: "Achievement", Sortable: true, SQLExpr: "a.text"},
		{Key: "metric", Label: "Metric", Sortable: false, SQLExpr: "a.metric", Width: "8rem"},
		{Key: "value", Label: "Value", Sortable: false, SQLExpr: "a.value", Width: "8rem"},
	},
	DefaultKey: "text",
	DefaultDir: admintable.Asc,
}

func achievementsResource(pool *pgxpool.Pool) resource.Resource {
	return resource.Resource{
		Name: "achievements", Title: "Achievements", Icon: "🏆", Group: grpResume,
		Sort:   achievementsSpec,
		Filter: admintable.FilterSpec{},
		Lister: achievementsLister(pool),
		FetchRow: func(ctx context.Context, id string) (map[string]string, error) {
			aid, err := strconv.Atoi(id)
			if err != nil {
				return nil, resource.ErrDetailNotFound
			}
			db := jobs.GetResumeDB()
			if db == nil {
				return nil, resource.ErrDetailNotFound
			}
			a, err := db.GetAchievementByID(ctx, aid)
			if err != nil {
				return nil, resource.ErrDetailNotFound
			}
			return map[string]string{"text": a.Text, "metric": a.Metric, "value": a.Value, "context": a.Context}, nil
		},
		Writer: &resource.Writer{
			Form: resource.FormSpec{Fields: []resource.Field{
				{Key: "text", Label: "Achievement", Kind: resource.FieldText, Required: true},
				{Key: "metric", Label: "Metric", Kind: resource.FieldText, Help: "e.g. revenue, uptime, users"},
				{Key: "value", Label: "Value", Kind: resource.FieldText, Help: "e.g. $2M, 99.9%, 10k"},
				{Key: "context", Label: "Context", Kind: resource.FieldTextarea},
			}},
			Load: func(ctx context.Context, _ tenant.Tenant, id string) (map[string]string, error) {
				aid, err := strconv.Atoi(id)
				if err != nil {
					return nil, resource.ErrDetailNotFound
				}
				db := jobs.GetResumeDB()
				if db == nil {
					return nil, resource.ErrDetailNotFound
				}
				a, err := db.GetAchievementByID(ctx, aid)
				if err != nil {
					return nil, resource.ErrDetailNotFound
				}
				return map[string]string{"text": a.Text, "metric": a.Metric, "value": a.Value, "context": a.Context}, nil
			},
			Save: func(ctx context.Context, _ tenant.Tenant, id string, v map[string]string) error {
				db := jobs.GetResumeDB()
				if db == nil {
					return resource.NewSaveError("text", "resume database not configured")
				}
				a := jobs.AchievementRecord{Text: v["text"], Metric: v["metric"], Value: v["value"], Context: v["context"]}
				if id == "" {
					pid := db.GetLatestPersonID(ctx)
					if pid == 0 {
						return resource.NewSaveError("text", "no resume person found")
					}
					_, err := db.InsertAchievement(ctx, pid, a)
					return err
				}
				aid, err := strconv.Atoi(id)
				if err != nil {
					return resource.NewSaveError("text", "invalid ID")
				}
				return db.UpdateAchievement(ctx, aid, a)
			},
			Delete: func(ctx context.Context, _ tenant.Tenant, id string) error {
				aid, err := strconv.Atoi(id)
				if err != nil {
					return resource.NewSaveError("text", "invalid ID")
				}
				db := jobs.GetResumeDB()
				if db == nil {
					return resource.NewSaveError("text", "resume database not configured")
				}
				return db.DeleteAchievement(ctx, aid)
			},
			RedirectAfterSave:   func(_ context.Context, _ string) string { return resumeEditURL },
			RedirectAfterDelete: func(_ context.Context, _ string) string { return resumeEditURL },
		},
	}
}

//nolint:dupl // structurally identical to other resume listers
func achievementsLister(pool *pgxpool.Pool) func(context.Context, resource.ListQuery) ([]resource.Row, int, error) {
	return func(ctx context.Context, _ resource.ListQuery) ([]resource.Row, int, error) {
		db := jobs.GetResumeDB()
		if db == nil {
			return nil, 0, nil
		}
		pid := db.GetLatestPersonID(ctx)
		if pid == 0 {
			return nil, 0, nil
		}
		items, err := db.GetAllAchievements(ctx, pid)
		if err != nil {
			slog.Error("achievementsLister", "err", err)
			return nil, 0, err
		}
		rows := make([]resource.Row, 0, len(items))
		for _, a := range items {
			rows = append(rows, resource.Row{
				ID: strconv.Itoa(a.ID),
				Cells: []resource.Cell{
					{Value: a.Text},
					{Value: a.Metric},
					{Value: a.Value},
				},
				Href: "/admin/achievements/" + strconv.Itoa(a.ID),
			})
		}
		return rows, len(rows), nil
	}
}

// --- Projects resource ---

var projectsSpec = admintable.Spec{
	Columns: []admintable.Column{
		{Key: "name", Label: "Project", Sortable: true, SQLExpr: "p.name"},
		{Key: "url", Label: "URL", Sortable: false, SQLExpr: "p.url"},
	},
	DefaultKey: "name",
	DefaultDir: admintable.Asc,
}

func projectsResource(pool *pgxpool.Pool) resource.Resource {
	return resource.Resource{
		Name: "projects", Title: "Projects", Icon: "📁", Group: grpResume,
		Sort:   projectsSpec,
		Filter: admintable.FilterSpec{},
		Lister: projectsLister(pool),
		FetchRow: func(ctx context.Context, id string) (map[string]string, error) {
			pid, err := strconv.Atoi(id)
			if err != nil {
				return nil, resource.ErrDetailNotFound
			}
			db := jobs.GetResumeDB()
			if db == nil {
				return nil, resource.ErrDetailNotFound
			}
			p, err := db.GetProjectByID(ctx, pid)
			if err != nil {
				return nil, resource.ErrDetailNotFound
			}
			return projectToMap(p), nil
		},
		Writer: &resource.Writer{
			Form: resource.FormSpec{Fields: []resource.Field{
				{Key: "name", Label: "Project Name", Kind: resource.FieldText, Required: true},
				{Key: "description", Label: "Description", Kind: resource.FieldTextarea},
				{Key: "url", Label: "URL", Kind: resource.FieldText},
				{Key: "tech", Label: "Tech Stack", Kind: resource.FieldJSON, Help: "JSON array, e.g. [\"Go\",\"Postgres\"]"},
				{Key: "highlights", Label: "Highlights", Kind: resource.FieldJSON, Help: "JSON array of strings"},
			}},
			Load: func(ctx context.Context, _ tenant.Tenant, id string) (map[string]string, error) {
				pid, err := strconv.Atoi(id)
				if err != nil {
					return nil, resource.ErrDetailNotFound
				}
				db := jobs.GetResumeDB()
				if db == nil {
					return nil, resource.ErrDetailNotFound
				}
				p, err := db.GetProjectByID(ctx, pid)
				if err != nil {
					return nil, resource.ErrDetailNotFound
				}
				return projectToMap(p), nil
			},
			Save: func(ctx context.Context, _ tenant.Tenant, id string, v map[string]string) error {
				db := jobs.GetResumeDB()
				if db == nil {
					return resource.NewSaveError("name", "resume database not configured")
				}
				p := jobs.ProjectRecord{
					Name: v["name"], Description: v["description"], URL: v["url"],
					Tech: parseHighlights(v["tech"]), Highlights: parseHighlights(v["highlights"]),
				}
				if id == "" {
					pid := db.GetLatestPersonID(ctx)
					if pid == 0 {
						return resource.NewSaveError("name", "no resume person found")
					}
					_, err := db.InsertProject(ctx, pid, p)
					return err
				}
				pid, err := strconv.Atoi(id)
				if err != nil {
					return resource.NewSaveError("name", "invalid ID")
				}
				return db.UpdateProject(ctx, pid, p)
			},
			Delete: func(ctx context.Context, _ tenant.Tenant, id string) error {
				pid, err := strconv.Atoi(id)
				if err != nil {
					return resource.NewSaveError("name", "invalid ID")
				}
				db := jobs.GetResumeDB()
				if db == nil {
					return resource.NewSaveError("name", "resume database not configured")
				}
				return db.DeleteProject(ctx, pid)
			},
			RedirectAfterSave:   func(_ context.Context, _ string) string { return resumeEditURL },
			RedirectAfterDelete: func(_ context.Context, _ string) string { return resumeEditURL },
		},
	}
}

//nolint:dupl // structurally identical to other resume listers
func projectsLister(pool *pgxpool.Pool) func(context.Context, resource.ListQuery) ([]resource.Row, int, error) {
	return func(ctx context.Context, _ resource.ListQuery) ([]resource.Row, int, error) {
		db := jobs.GetResumeDB()
		if db == nil {
			return nil, 0, nil
		}
		pid := db.GetLatestPersonID(ctx)
		if pid == 0 {
			return nil, 0, nil
		}
		items, err := db.GetAllProjects(ctx, pid)
		if err != nil {
			slog.Error("projectsLister", "err", err)
			return nil, 0, err
		}
		rows := make([]resource.Row, 0, len(items))
		for _, p := range items {
			rows = append(rows, resource.Row{
				ID: strconv.Itoa(p.ID),
				Cells: []resource.Cell{
					{Value: p.Name},
					{Value: p.URL},
				},
				Href: "/admin/projects/" + strconv.Itoa(p.ID),
			})
		}
		return rows, len(rows), nil
	}
}

func projectToMap(p jobs.ProjectRecord) map[string]string {
	techJSON := "[]"
	if len(p.Tech) > 0 {
		if b, err := json.Marshal(p.Tech); err == nil {
			techJSON = string(b)
		}
	}
	highlightsJSON := "[]"
	if len(p.Highlights) > 0 {
		if b, err := json.Marshal(p.Highlights); err == nil {
			highlightsJSON = string(b)
		}
	}
	return map[string]string{
		"name": p.Name, "description": p.Description, "url": p.URL,
		"tech": techJSON, "highlights": highlightsJSON,
	}
}

// --- Educations resource ---

var educationsSpec = admintable.Spec{
	Columns: []admintable.Column{
		{Key: "school", Label: "School", Sortable: true, SQLExpr: "ed.school"},
		{Key: "degree", Label: "Degree", Sortable: true, SQLExpr: "ed.degree"},
		{Key: "field", Label: "Field", Sortable: false, SQLExpr: "ed.field"},
	},
	DefaultKey: "school",
	DefaultDir: admintable.Asc,
}

func educationsResource(pool *pgxpool.Pool) resource.Resource {
	return resource.Resource{
		Name: "educations", Title: "Education", Icon: "🎓", Group: grpResume,
		Sort:   educationsSpec,
		Filter: admintable.FilterSpec{},
		Lister: educationsLister(pool),
		FetchRow: func(ctx context.Context, id string) (map[string]string, error) {
			eid, err := strconv.Atoi(id)
			if err != nil {
				return nil, resource.ErrDetailNotFound
			}
			db := jobs.GetResumeDB()
			if db == nil {
				return nil, resource.ErrDetailNotFound
			}
			e, err := db.GetEducationByID(ctx, eid)
			if err != nil {
				return nil, resource.ErrDetailNotFound
			}
			return educationToMap(e), nil
		},
		Writer: &resource.Writer{
			Form: resource.FormSpec{Fields: []resource.Field{
				{Key: "school", Label: "School", Kind: resource.FieldText, Required: true},
				{Key: "degree", Label: "Degree", Kind: resource.FieldText},
				{Key: "field", Label: "Field of Study", Kind: resource.FieldText},
				{Key: "start_date", Label: "Start", Kind: resource.FieldText, Help: "YYYY-MM-DD or text"},
				{Key: "end_date", Label: "End", Kind: resource.FieldText, Help: "YYYY-MM-DD or text"},
				{Key: "gpa", Label: "GPA", Kind: resource.FieldText},
				{Key: "highlights", Label: "Highlights", Kind: resource.FieldJSON, Help: "JSON array of strings"},
			}},
			Load: func(ctx context.Context, _ tenant.Tenant, id string) (map[string]string, error) {
				eid, err := strconv.Atoi(id)
				if err != nil {
					return nil, resource.ErrDetailNotFound
				}
				db := jobs.GetResumeDB()
				if db == nil {
					return nil, resource.ErrDetailNotFound
				}
				e, err := db.GetEducationByID(ctx, eid)
				if err != nil {
					return nil, resource.ErrDetailNotFound
				}
				return educationToMap(e), nil
			},
			Save: func(ctx context.Context, _ tenant.Tenant, id string, v map[string]string) error {
				db := jobs.GetResumeDB()
				if db == nil {
					return resource.NewSaveError("school", "resume database not configured")
				}
				e := jobs.EducationRecord{
					School: v["school"], Degree: v["degree"], Field: v["field"],
					StartDate: v["start_date"], EndDate: v["end_date"], GPA: v["gpa"],
					Highlights: parseHighlights(v["highlights"]),
				}
				if id == "" {
					pid := db.GetLatestPersonID(ctx)
					if pid == 0 {
						return resource.NewSaveError("school", "no resume person found")
					}
					_, err := db.InsertEducation(ctx, pid, e)
					return err
				}
				eid, err := strconv.Atoi(id)
				if err != nil {
					return resource.NewSaveError("school", "invalid ID")
				}
				return db.UpdateEducation(ctx, eid, e)
			},
			Delete: func(ctx context.Context, _ tenant.Tenant, id string) error {
				eid, err := strconv.Atoi(id)
				if err != nil {
					return resource.NewSaveError("school", "invalid ID")
				}
				db := jobs.GetResumeDB()
				if db == nil {
					return resource.NewSaveError("school", "resume database not configured")
				}
				return db.DeleteEducation(ctx, eid)
			},
			RedirectAfterSave:   func(_ context.Context, _ string) string { return resumeEditURL },
			RedirectAfterDelete: func(_ context.Context, _ string) string { return resumeEditURL },
		},
	}
}

//nolint:dupl // structurally identical to other resume listers
func educationsLister(pool *pgxpool.Pool) func(context.Context, resource.ListQuery) ([]resource.Row, int, error) {
	return func(ctx context.Context, _ resource.ListQuery) ([]resource.Row, int, error) {
		db := jobs.GetResumeDB()
		if db == nil {
			return nil, 0, nil
		}
		pid := db.GetLatestPersonID(ctx)
		if pid == 0 {
			return nil, 0, nil
		}
		items, err := db.GetAllEducations(ctx, pid)
		if err != nil {
			slog.Error("educationsLister", "err", err)
			return nil, 0, err
		}
		rows := make([]resource.Row, 0, len(items))
		for _, e := range items {
			rows = append(rows, resource.Row{
				ID: strconv.Itoa(e.ID),
				Cells: []resource.Cell{
					{Value: e.School},
					{Value: e.Degree},
					{Value: e.Field},
				},
				Href: "/admin/educations/" + strconv.Itoa(e.ID),
			})
		}
		return rows, len(rows), nil
	}
}

func educationToMap(e jobs.EducationRecord) map[string]string {
	highlightsJSON := "[]"
	if len(e.Highlights) > 0 {
		if b, err := json.Marshal(e.Highlights); err == nil {
			highlightsJSON = string(b)
		}
	}
	return map[string]string{
		"school": e.School, "degree": e.Degree, "field": e.Field,
		"start_date": e.StartDate, "end_date": e.EndDate, "gpa": e.GPA,
		"highlights": highlightsJSON,
	}
}

// --- Certifications resource ---

var certificationsSpec = admintable.Spec{
	Columns: []admintable.Column{
		{Key: "name", Label: "Certification", Sortable: true, SQLExpr: "c.name"},
		{Key: "issuer", Label: "Issuer", Sortable: true, SQLExpr: "c.issuer"},
		{Key: "year", Label: "Year", Sortable: true, SQLExpr: "c.year", Width: "6rem"},
	},
	DefaultKey: "name",
	DefaultDir: admintable.Asc,
}

func certificationsResource(pool *pgxpool.Pool) resource.Resource {
	return resource.Resource{
		Name: "certifications", Title: "Certifications", Icon: "📜", Group: grpResume,
		Sort:   certificationsSpec,
		Filter: admintable.FilterSpec{},
		Lister: certificationsLister(pool),
		FetchRow: func(ctx context.Context, id string) (map[string]string, error) {
			cid, err := strconv.Atoi(id)
			if err != nil {
				return nil, resource.ErrDetailNotFound
			}
			db := jobs.GetResumeDB()
			if db == nil {
				return nil, resource.ErrDetailNotFound
			}
			c, err := db.GetCertificationByID(ctx, cid)
			if err != nil {
				return nil, resource.ErrDetailNotFound
			}
			return map[string]string{"name": c.Name, "issuer": c.Issuer, "year": c.Year, "url": c.URL}, nil
		},
		Writer: &resource.Writer{
			Form: resource.FormSpec{Fields: []resource.Field{
				{Key: "name", Label: "Certification", Kind: resource.FieldText, Required: true},
				{Key: "issuer", Label: "Issuer", Kind: resource.FieldText},
				{Key: "year", Label: "Year", Kind: resource.FieldText},
				{Key: "url", Label: "URL", Kind: resource.FieldText},
			}},
			Load: func(ctx context.Context, _ tenant.Tenant, id string) (map[string]string, error) {
				cid, err := strconv.Atoi(id)
				if err != nil {
					return nil, resource.ErrDetailNotFound
				}
				db := jobs.GetResumeDB()
				if db == nil {
					return nil, resource.ErrDetailNotFound
				}
				c, err := db.GetCertificationByID(ctx, cid)
				if err != nil {
					return nil, resource.ErrDetailNotFound
				}
				return map[string]string{"name": c.Name, "issuer": c.Issuer, "year": c.Year, "url": c.URL}, nil
			},
			Save: func(ctx context.Context, _ tenant.Tenant, id string, v map[string]string) error {
				db := jobs.GetResumeDB()
				if db == nil {
					return resource.NewSaveError("name", "resume database not configured")
				}
				c := jobs.CertificationRecord{Name: v["name"], Issuer: v["issuer"], Year: v["year"], URL: v["url"]}
				if id == "" {
					pid := db.GetLatestPersonID(ctx)
					if pid == 0 {
						return resource.NewSaveError("name", "no resume person found")
					}
					_, err := db.InsertCertification(ctx, pid, c)
					return err
				}
				cid, err := strconv.Atoi(id)
				if err != nil {
					return resource.NewSaveError("name", "invalid ID")
				}
				return db.UpdateCertification(ctx, cid, c)
			},
			Delete: func(ctx context.Context, _ tenant.Tenant, id string) error {
				cid, err := strconv.Atoi(id)
				if err != nil {
					return resource.NewSaveError("name", "invalid ID")
				}
				db := jobs.GetResumeDB()
				if db == nil {
					return resource.NewSaveError("name", "resume database not configured")
				}
				return db.DeleteCertification(ctx, cid)
			},
			RedirectAfterSave:   func(_ context.Context, _ string) string { return resumeEditURL },
			RedirectAfterDelete: func(_ context.Context, _ string) string { return resumeEditURL },
		},
	}
}

//nolint:dupl // structurally identical to other resume listers
func certificationsLister(pool *pgxpool.Pool) func(context.Context, resource.ListQuery) ([]resource.Row, int, error) {
	return func(ctx context.Context, _ resource.ListQuery) ([]resource.Row, int, error) {
		db := jobs.GetResumeDB()
		if db == nil {
			return nil, 0, nil
		}
		pid := db.GetLatestPersonID(ctx)
		if pid == 0 {
			return nil, 0, nil
		}
		items, err := db.GetAllCertifications(ctx, pid)
		if err != nil {
			slog.Error("certificationsLister", "err", err)
			return nil, 0, err
		}
		rows := make([]resource.Row, 0, len(items))
		for _, c := range items {
			rows = append(rows, resource.Row{
				ID: strconv.Itoa(c.ID),
				Cells: []resource.Cell{
					{Value: c.Name},
					{Value: c.Issuer},
					{Value: c.Year},
				},
				Href: "/admin/certifications/" + strconv.Itoa(c.ID),
			})
		}
		return rows, len(rows), nil
	}
}

// --- Domains resource ---

var domainsSpec = admintable.Spec{
	Columns: []admintable.Column{
		{Key: "name", Label: "Domain", Sortable: true, SQLExpr: "d.name"},
	},
	DefaultKey: "name",
	DefaultDir: admintable.Asc,
}

func domainsResource(pool *pgxpool.Pool) resource.Resource {
	return resource.Resource{
		Name: "domains", Title: "Domains", Icon: "🌐", Group: grpResume,
		Sort:   domainsSpec,
		Filter: admintable.FilterSpec{},
		Lister: domainsLister(pool),
		FetchRow: func(ctx context.Context, id string) (map[string]string, error) {
			did, err := strconv.Atoi(id)
			if err != nil {
				return nil, resource.ErrDetailNotFound
			}
			db := jobs.GetResumeDB()
			if db == nil {
				return nil, resource.ErrDetailNotFound
			}
			d, err := db.GetDomainByID(ctx, did)
			if err != nil {
				return nil, resource.ErrDetailNotFound
			}
			return map[string]string{"name": d.Name}, nil
		},
		Writer: &resource.Writer{
			Form: resource.FormSpec{Fields: []resource.Field{
				{Key: "name", Label: "Domain", Kind: resource.FieldText, Required: true, Help: "e.g. Fintech, Healthcare, E-commerce"},
			}},
			Load: func(ctx context.Context, _ tenant.Tenant, id string) (map[string]string, error) {
				did, err := strconv.Atoi(id)
				if err != nil {
					return nil, resource.ErrDetailNotFound
				}
				db := jobs.GetResumeDB()
				if db == nil {
					return nil, resource.ErrDetailNotFound
				}
				d, err := db.GetDomainByID(ctx, did)
				if err != nil {
					return nil, resource.ErrDetailNotFound
				}
				return map[string]string{"name": d.Name}, nil
			},
			Save: func(ctx context.Context, _ tenant.Tenant, id string, v map[string]string) error {
				db := jobs.GetResumeDB()
				if db == nil {
					return resource.NewSaveError("name", "resume database not configured")
				}
				name := v["name"]
				if id == "" {
					pid := db.GetLatestPersonID(ctx)
					if pid == 0 {
						return resource.NewSaveError("name", "no resume person found")
					}
					_, err := db.InsertDomain(ctx, pid, name)
					return err
				}
				did, err := strconv.Atoi(id)
				if err != nil {
					return resource.NewSaveError("name", "invalid ID")
				}
				return db.UpdateDomain(ctx, did, name)
			},
			Delete: func(ctx context.Context, _ tenant.Tenant, id string) error {
				did, err := strconv.Atoi(id)
				if err != nil {
					return resource.NewSaveError("name", "invalid ID")
				}
				db := jobs.GetResumeDB()
				if db == nil {
					return resource.NewSaveError("name", "resume database not configured")
				}
				return db.DeleteDomain(ctx, did)
			},
			RedirectAfterSave:   func(_ context.Context, _ string) string { return resumeEditURL },
			RedirectAfterDelete: func(_ context.Context, _ string) string { return resumeEditURL },
		},
	}
}

func domainsLister(pool *pgxpool.Pool) func(context.Context, resource.ListQuery) ([]resource.Row, int, error) {
	return func(ctx context.Context, _ resource.ListQuery) ([]resource.Row, int, error) {
		db := jobs.GetResumeDB()
		if db == nil {
			return nil, 0, nil
		}
		pid := db.GetLatestPersonID(ctx)
		if pid == 0 {
			return nil, 0, nil
		}
		items, err := db.GetAllDomains(ctx, pid)
		if err != nil {
			slog.Error("domainsLister", "err", err)
			return nil, 0, err
		}
		rows := make([]resource.Row, 0, len(items))
		for _, d := range items {
			rows = append(rows, resource.Row{
				ID:    strconv.Itoa(d.ID),
				Cells: []resource.Cell{{Value: d.Name}},
				Href:  "/admin/domains/" + strconv.Itoa(d.ID),
			})
		}
		return rows, len(rows), nil
	}
}

// --- Methodologies resource ---

var methodologiesSpec = admintable.Spec{
	Columns: []admintable.Column{
		{Key: "name", Label: "Methodology", Sortable: true, SQLExpr: "m.name"},
		{Key: "description", Label: "Description", Sortable: false, SQLExpr: "m.description"},
	},
	DefaultKey: "name",
	DefaultDir: admintable.Asc,
}

func methodologiesResource(pool *pgxpool.Pool) resource.Resource {
	return resource.Resource{
		Name: "methodologies", Title: "Methodologies", Icon: "⚙️", Group: grpResume,
		Sort:   methodologiesSpec,
		Filter: admintable.FilterSpec{},
		Lister: methodologiesLister(pool),
		FetchRow: func(ctx context.Context, id string) (map[string]string, error) {
			mid, err := strconv.Atoi(id)
			if err != nil {
				return nil, resource.ErrDetailNotFound
			}
			db := jobs.GetResumeDB()
			if db == nil {
				return nil, resource.ErrDetailNotFound
			}
			m, err := db.GetMethodologyByID(ctx, mid)
			if err != nil {
				return nil, resource.ErrDetailNotFound
			}
			return map[string]string{"name": m.Name, "description": m.Description}, nil
		},
		Writer: &resource.Writer{
			Form: resource.FormSpec{Fields: []resource.Field{
				{Key: "name", Label: "Methodology", Kind: resource.FieldText, Required: true, Help: "e.g. Agile, Kanban, Trunk-based development"},
				{Key: "description", Label: "Description", Kind: resource.FieldTextarea},
			}},
			Load: func(ctx context.Context, _ tenant.Tenant, id string) (map[string]string, error) {
				mid, err := strconv.Atoi(id)
				if err != nil {
					return nil, resource.ErrDetailNotFound
				}
				db := jobs.GetResumeDB()
				if db == nil {
					return nil, resource.ErrDetailNotFound
				}
				m, err := db.GetMethodologyByID(ctx, mid)
				if err != nil {
					return nil, resource.ErrDetailNotFound
				}
				return map[string]string{"name": m.Name, "description": m.Description}, nil
			},
			Save: func(ctx context.Context, _ tenant.Tenant, id string, v map[string]string) error {
				db := jobs.GetResumeDB()
				if db == nil {
					return resource.NewSaveError("name", "resume database not configured")
				}
				name, desc := v["name"], v["description"]
				if id == "" {
					pid := db.GetLatestPersonID(ctx)
					if pid == 0 {
						return resource.NewSaveError("name", "no resume person found")
					}
					_, err := db.InsertMethodology(ctx, pid, name, desc)
					return err
				}
				mid, err := strconv.Atoi(id)
				if err != nil {
					return resource.NewSaveError("name", "invalid ID")
				}
				return db.UpdateMethodology(ctx, mid, name, desc)
			},
			Delete: func(ctx context.Context, _ tenant.Tenant, id string) error {
				mid, err := strconv.Atoi(id)
				if err != nil {
					return resource.NewSaveError("name", "invalid ID")
				}
				db := jobs.GetResumeDB()
				if db == nil {
					return resource.NewSaveError("name", "resume database not configured")
				}
				return db.DeleteMethodology(ctx, mid)
			},
			RedirectAfterSave:   func(_ context.Context, _ string) string { return resumeEditURL },
			RedirectAfterDelete: func(_ context.Context, _ string) string { return resumeEditURL },
		},
	}
}

//nolint:dupl // structurally identical to other resume listers
func methodologiesLister(pool *pgxpool.Pool) func(context.Context, resource.ListQuery) ([]resource.Row, int, error) {
	return func(ctx context.Context, _ resource.ListQuery) ([]resource.Row, int, error) {
		db := jobs.GetResumeDB()
		if db == nil {
			return nil, 0, nil
		}
		pid := db.GetLatestPersonID(ctx)
		if pid == 0 {
			return nil, 0, nil
		}
		items, err := db.GetAllMethodologies(ctx, pid)
		if err != nil {
			slog.Error("methodologiesLister", "err", err)
			return nil, 0, err
		}
		rows := make([]resource.Row, 0, len(items))
		for _, m := range items {
			rows = append(rows, resource.Row{
				ID: strconv.Itoa(m.ID),
				Cells: []resource.Cell{
					{Value: m.Name},
					{Value: m.Description},
				},
				Href: "/admin/methodologies/" + strconv.Itoa(m.ID),
			})
		}
		return rows, len(rows), nil
	}
}
