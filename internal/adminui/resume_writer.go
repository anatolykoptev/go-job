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
const grpResume = "Resume"

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
				return "/admin/resume/edit"
			},
			RedirectAfterDelete: func(_ context.Context, _ string) string {
				return "/admin/resume/edit"
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
