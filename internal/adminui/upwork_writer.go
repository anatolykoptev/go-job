package adminui

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"

	"github.com/anatolykoptev/go-kit/admintable"
	"github.com/anatolykoptev/go-panel/resource"
	"github.com/anatolykoptev/go-panel/tenant"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/jackc/pgx/v5/pgxpool"
)

// grpUpwork is the sidebar group label for upwork-related resources.
const grpUpwork = "Upwork"

// upworkEditRedirect is the redirect target after upwork entity mutations.
const upworkEditRedirect = "/admin/upwork"

// --- Upwork overview (SingleRow) ---

var upworkOverviewSpec = admintable.Spec{
	Columns: []admintable.Column{
		{Key: "title", Label: "Title", Sortable: true, SQLExpr: "up.title"},
		{Key: "hourly_rate", Label: "Rate", Sortable: false, SQLExpr: "up.hourly_rate", Width: "6rem"},
		{Key: "availability", Label: "Availability", Sortable: false, SQLExpr: "up.availability", Width: "8rem"},
	},
	DefaultKey: "title",
	DefaultDir: admintable.Asc,
}

func upworkOverviewResource(pool *pgxpool.Pool) resource.Resource {
	return resource.Resource{
		Name:   "upwork_overview",
		Title:  "Upwork Overview",
		Icon:   "📝",
		Group:  grpUpwork,
		Sort:   upworkOverviewSpec,
		Filter: admintable.FilterSpec{},
		Lister: upworkOverviewLister(pool),
		FetchRow: func(ctx context.Context, id string) (map[string]string, error) {
			db := jobs.GetResumeDB()
			if db == nil {
				return nil, resource.ErrDetailNotFound
			}
			pid := db.GetLatestPersonID(ctx)
			if pid == 0 {
				return nil, resource.ErrDetailNotFound
			}
			result, err := db.GetUpworkProfile(ctx, pid)
			if err != nil || result == nil || result.Missing {
				return nil, resource.ErrDetailNotFound
			}
			return upworkOverviewToMap(result.Profile), nil
		},
		Writer: &resource.Writer{
			Form: resource.FormSpec{Fields: []resource.Field{
				{Key: "title", Label: "Title / Headline", Kind: resource.FieldText, Required: true},
				{Key: "overview", Label: "Professional Overview", Kind: resource.FieldTextarea},
				{Key: "hourly_rate", Label: "Hourly Rate (USD)", Kind: resource.FieldText, Help: "e.g. 85, 120.50"},
				{Key: "categories", Label: "Categories", Kind: resource.FieldJSON, Help: "JSON array, e.g. [\"Web Development\",\"API Integration\"]"},
				{Key: "availability", Label: "Availability", Kind: resource.FieldText, Help: "e.g. Full-time, Part-time"},
			}},
			Load: func(ctx context.Context, _ tenant.Tenant, _ string) (map[string]string, error) {
				db := jobs.GetResumeDB()
				if db == nil {
					return nil, resource.ErrDetailNotFound
				}
				pid := db.GetLatestPersonID(ctx)
				if pid == 0 {
					return nil, resource.ErrDetailNotFound
				}
				result, err := db.GetUpworkProfile(ctx, pid)
				if err != nil || result == nil || result.Missing {
					return nil, resource.ErrDetailNotFound
				}
				return upworkOverviewToMap(result.Profile), nil
			},
			Save: func(ctx context.Context, _ tenant.Tenant, _ string, v map[string]string) error {
				db := jobs.GetResumeDB()
				if db == nil {
					return resource.NewSaveError("title", "resume database not configured")
				}
				pid := db.GetLatestPersonID(ctx)
				if pid == 0 {
					return resource.NewSaveError("title", "no resume person found")
				}
				hourlyRateCents, rateErr := parseDollarsToCents(v["hourly_rate"])
				if rateErr != nil {
					return resource.NewSaveError("hourly_rate", rateErr.Error())
				}
				categories := parseHighlights(v["categories"])
				return db.UpsertUpworkProfile(ctx, pid, v["title"], v["overview"], hourlyRateCents, categories, v["availability"])
			},
			RedirectAfterSave: func(_ context.Context, _ string) string { return upworkEditRedirect },
		},
	}
}

//nolint:dupl // structurally identical to other listers
func upworkOverviewLister(pool *pgxpool.Pool) func(context.Context, resource.ListQuery) ([]resource.Row, int, error) {
	return func(ctx context.Context, _ resource.ListQuery) ([]resource.Row, int, error) {
		db := jobs.GetResumeDB()
		if db == nil {
			return nil, 0, nil
		}
		pid := db.GetLatestPersonID(ctx)
		if pid == 0 {
			return nil, 0, nil
		}
		result, err := db.GetUpworkProfile(ctx, pid)
		if err != nil || result == nil || result.Missing || result.Profile == nil {
			return nil, 0, nil
		}
		p := result.Profile
		rows := []resource.Row{{
			ID: strconv.Itoa(pid),
			Cells: []resource.Cell{
				{Value: p.Title},
				{Value: formatCentsToDollars(p.HourlyRate)},
				{Value: p.Availability},
			},
			Href: "/admin/upwork_overview/" + strconv.Itoa(pid) + "/edit",
		}}
		return rows, 1, nil
	}
}

func upworkOverviewToMap(p *jobs.UpworkProfile) map[string]string {
	categoriesJSON := "[]"
	if len(p.Categories) > 0 {
		if b, err := json.Marshal(p.Categories); err == nil {
			categoriesJSON = string(b)
		}
	}
	hourlyRate := ""
	if p.HourlyRate > 0 {
		hourlyRate = formatCentsToDollars(p.HourlyRate)
	}
	return map[string]string{
		"title":        p.Title,
		"overview":     p.Overview,
		"hourly_rate":  hourlyRate,
		"categories":   categoriesJSON,
		"availability": p.Availability,
	}
}

// --- Upwork skills ---

var upworkSkillsSpec = admintable.Spec{
	Columns: []admintable.Column{
		{Key: "name", Label: "Skill", Sortable: true, SQLExpr: "us.name"},
	},
	DefaultKey: "name",
	DefaultDir: admintable.Asc,
}

func upworkSkillsResource(pool *pgxpool.Pool) resource.Resource {
	return resource.Resource{
		Name:   "upwork_skills",
		Title:  "Upwork Skills",
		Icon:   "🛠",
		Group:  grpUpwork,
		Sort:   upworkSkillsSpec,
		Filter: admintable.FilterSpec{},
		Lister: upworkSkillsLister(pool),
		FetchRow: func(ctx context.Context, id string) (map[string]string, error) {
			sid, err := strconv.Atoi(id)
			if err != nil {
				return nil, resource.ErrDetailNotFound
			}
			db := jobs.GetResumeDB()
			if db == nil {
				return nil, resource.ErrDetailNotFound
			}
			s, err := db.GetUpworkSkillByID(ctx, sid)
			if err != nil {
				return nil, resource.ErrDetailNotFound
			}
			return map[string]string{"name": s.Name}, nil
		},
		Writer: &resource.Writer{
			Form: resource.FormSpec{Fields: []resource.Field{
				{Key: "name", Label: "Skill", Kind: resource.FieldText, Required: true},
			}},
			Load: func(ctx context.Context, _ tenant.Tenant, id string) (map[string]string, error) {
				sid, err := strconv.Atoi(id)
				if err != nil {
					return nil, resource.ErrDetailNotFound
				}
				db := jobs.GetResumeDB()
				if db == nil {
					return nil, resource.ErrDetailNotFound
				}
				s, err := db.GetUpworkSkillByID(ctx, sid)
				if err != nil {
					return nil, resource.ErrDetailNotFound
				}
				return map[string]string{"name": s.Name}, nil
			},
			Save: func(ctx context.Context, _ tenant.Tenant, id string, v map[string]string) error {
				db := jobs.GetResumeDB()
				if db == nil {
					return resource.NewSaveError("name", "resume database not configured")
				}
				pid := db.GetLatestPersonID(ctx)
				if pid == 0 {
					return resource.NewSaveError("name", "no resume person found")
				}
				name := v["name"]
				if id == "" {
					_, err := db.InsertUpworkSkill(ctx, pid, name)
					return err
				}
				sid, err := strconv.Atoi(id)
				if err != nil {
					return resource.NewSaveError("name", "invalid ID")
				}
				return db.UpdateUpworkSkill(ctx, sid, name)
			},
			Delete: func(ctx context.Context, _ tenant.Tenant, id string) error {
				sid, err := strconv.Atoi(id)
				if err != nil {
					return resource.NewSaveError("name", "invalid ID")
				}
				db := jobs.GetResumeDB()
				if db == nil {
					return resource.NewSaveError("name", "resume database not configured")
				}
				pid := db.GetLatestPersonID(ctx)
				if pid == 0 {
					return resource.NewSaveError("name", "no resume person found")
				}
				return db.DeleteUpworkSkill(ctx, pid, sid)
			},
			RedirectAfterSave:   func(_ context.Context, _ string) string { return upworkEditRedirect },
			RedirectAfterDelete: func(_ context.Context, _ string) string { return upworkEditRedirect },
		},
	}
}

//nolint:dupl // structurally identical to other listers
func upworkSkillsLister(pool *pgxpool.Pool) func(context.Context, resource.ListQuery) ([]resource.Row, int, error) {
	return func(ctx context.Context, _ resource.ListQuery) ([]resource.Row, int, error) {
		db := jobs.GetResumeDB()
		if db == nil {
			return nil, 0, nil
		}
		pid := db.GetLatestPersonID(ctx)
		if pid == 0 {
			return nil, 0, nil
		}
		result, err := db.GetUpworkProfile(ctx, pid)
		if err != nil || result == nil {
			return nil, 0, nil
		}
		rows := make([]resource.Row, 0, len(result.Skills))
		for _, s := range result.Skills {
			rows = append(rows, resource.Row{
				ID:    strconv.Itoa(s.ID),
				Cells: []resource.Cell{{Value: s.Name}},
				Href:  "/admin/upwork_skills/" + strconv.Itoa(s.ID),
			})
		}
		return rows, len(rows), nil
	}
}

// --- Upwork catalog items ---

var upworkCatalogSpec = admintable.Spec{
	Columns: []admintable.Column{
		{Key: "title", Label: "Title", Sortable: true, SQLExpr: "uc.title"},
		{Key: "description", Label: "Description", Sortable: false, SQLExpr: "uc.description"},
	},
	DefaultKey: "title",
	DefaultDir: admintable.Asc,
}

func upworkCatalogResource(pool *pgxpool.Pool) resource.Resource {
	return resource.Resource{
		Name:   "upwork_catalog",
		Title:  "Portfolio Items",
		Icon:   "📂",
		Group:  grpUpwork,
		Sort:   upworkCatalogSpec,
		Filter: admintable.FilterSpec{},
		Lister: upworkCatalogLister(pool),
		FetchRow: func(ctx context.Context, id string) (map[string]string, error) {
			cid, err := strconv.Atoi(id)
			if err != nil {
				return nil, resource.ErrDetailNotFound
			}
			db := jobs.GetResumeDB()
			if db == nil {
				return nil, resource.ErrDetailNotFound
			}
			c, err := db.GetUpworkCatalogItemByID(ctx, cid)
			if err != nil {
				return nil, resource.ErrDetailNotFound
			}
			return map[string]string{"title": c.Title, "description": c.Description}, nil
		},
		Writer: &resource.Writer{
			Form: resource.FormSpec{Fields: []resource.Field{
				{Key: "title", Label: "Title", Kind: resource.FieldText, Required: true},
				{Key: "description", Label: "Description", Kind: resource.FieldTextarea},
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
				c, err := db.GetUpworkCatalogItemByID(ctx, cid)
				if err != nil {
					return nil, resource.ErrDetailNotFound
				}
				return map[string]string{"title": c.Title, "description": c.Description}, nil
			},
			Save: func(ctx context.Context, _ tenant.Tenant, id string, v map[string]string) error {
				db := jobs.GetResumeDB()
				if db == nil {
					return resource.NewSaveError("title", "resume database not configured")
				}
				pid := db.GetLatestPersonID(ctx)
				if pid == 0 {
					return resource.NewSaveError("title", "no resume person found")
				}
				title, desc := v["title"], v["description"]
				if id == "" {
					_, err := db.InsertUpworkCatalogItem(ctx, pid, title, desc)
					return err
				}
				cid, err := strconv.Atoi(id)
				if err != nil {
					return resource.NewSaveError("title", "invalid ID")
				}
				return db.UpdateUpworkCatalogItem(ctx, cid, title, desc)
			},
			Delete: func(ctx context.Context, _ tenant.Tenant, id string) error {
				cid, err := strconv.Atoi(id)
				if err != nil {
					return resource.NewSaveError("title", "invalid ID")
				}
				db := jobs.GetResumeDB()
				if db == nil {
					return resource.NewSaveError("title", "resume database not configured")
				}
				pid := db.GetLatestPersonID(ctx)
				if pid == 0 {
					return resource.NewSaveError("title", "no resume person found")
				}
				return db.DeleteUpworkCatalogItem(ctx, pid, cid)
			},
			RedirectAfterSave:   func(_ context.Context, _ string) string { return upworkEditRedirect },
			RedirectAfterDelete: func(_ context.Context, _ string) string { return upworkEditRedirect },
		},
	}
}

//nolint:dupl // structurally identical to other listers
func upworkCatalogLister(pool *pgxpool.Pool) func(context.Context, resource.ListQuery) ([]resource.Row, int, error) {
	return func(ctx context.Context, _ resource.ListQuery) ([]resource.Row, int, error) {
		db := jobs.GetResumeDB()
		if db == nil {
			return nil, 0, nil
		}
		pid := db.GetLatestPersonID(ctx)
		if pid == 0 {
			return nil, 0, nil
		}
		result, err := db.GetUpworkProfile(ctx, pid)
		if err != nil || result == nil {
			slog.Error("upworkCatalogLister", "err", err)
			return nil, 0, err
		}
		rows := make([]resource.Row, 0, len(result.Catalog))
		for _, c := range result.Catalog {
			rows = append(rows, resource.Row{
				ID: strconv.Itoa(c.ID),
				Cells: []resource.Cell{
					{Value: c.Title},
					{Value: c.Description},
				},
				Href: "/admin/upwork_catalog/" + strconv.Itoa(c.ID),
			})
		}
		return rows, len(rows), nil
	}
}
