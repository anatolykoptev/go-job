package adminui

import (
	"context"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strconv"

	"github.com/anatolykoptev/go-kit/admintable"
	"github.com/anatolykoptev/go-panel/resource"
	"github.com/anatolykoptev/go_job/internal/hunt"
)

// navIDShortlist is the sidebar nav ID for the curated shortlist page.
// It matches resource.Resource.Name so go-panel auto-routes /admin/shortlist.
const navIDShortlist = "shortlist"

// Badge CSS modifier classes (goconst threshold ≥4 across stageBadgeClass + tests).
const (
	cssBadgeBlue  = "badge-blue"
	cssBadgeGreen = "badge-green"
)

// shortlistActiveStages is the curated set: jobs with a hunt_ratings row in one
// of these stages appear on the shortlist. Excludes new/discarded/rejected which
// are noise or terminal-negative.
var shortlistActiveStages = []string{
	hunt.StageInteresting,
	hunt.StageSaved,
	hunt.StageClaimed,
	hunt.StageApplied,
	hunt.StageInterview,
	hunt.StageOffer,
}

// ── resource.Resource path (postgres) ─────────────────────────────────────────

// shortlistSpec drives the /admin/shortlist table sort/columns. Cell order in the
// Lister MUST match Columns order.
//
// IMPORTANT: cell index 0 is the Href-linked cell in go-panel's list template.
// The template wraps cell-0 in <a href=…>{EscapeString(value)}</a> and ignores
// cell.HTML for that index. Therefore cell-0 MUST be plain text (Title · Company).
// Stage/fit/market/docs badges are at i>0 where cell.HTML is respected.
var shortlistSpec = admintable.Spec{
	Columns: []admintable.Column{
		{Key: colKeyTitle, Label: "Title / Company", Sortable: true, SQLExpr: "j.title"},
		{Key: "stage", Label: "Stage", Sortable: true, SQLExpr: "r.stage", Width: colWidth8rem},
		{Key: colKeyFit, Label: "Fit", Sortable: true, SQLExpr: "j.fit_score", NullsLast: true, TieBreakSQLExpr: "j.company", Width: colWidth8rem},
		{Key: "market", Label: "Market", Sortable: true, SQLExpr: "CASE j.success_band WHEN 'STRONG' THEN 3 WHEN 'MODERATE' THEN 2 WHEN 'LONGSHOT' THEN 1 ELSE 0 END", NullsLast: true, Width: "11rem"},
		{Key: "comp", Label: "Comp", Sortable: false},
		{Key: "docs", Label: "Docs", Sortable: false, Width: colWidth8rem},
		{Key: "rated", Label: "Rated", Sortable: true, SQLExpr: "r.rated_at", Width: "6rem"},
	},
	DefaultKey: colKeyFit,
	DefaultDir: admintable.Desc,
}

// shortlistFilter declares the /admin/shortlist filter bar.
// PDF-derived filters (pack-ready, with-docs) cannot be expressed as SQL — they
// are surfaced as badges in the Docs cell instead. Stage and text-search filters
// are SQL-backed via the FilterSpec.
var shortlistFilter = admintable.FilterSpec{Filters: []admintable.Filter{
	{Key: keyQ, SQLExprs: []string{"j.title", "j.company"}, Match: admintable.ILike},
	{Key: "stage", SQLExpr: "r.stage", Match: admintable.Eq, Allowed: shortlistActiveStages},
}}

func shortlistResource(store *hunt.Store, adminUser, applicationsDir string) resource.Resource {
	return resource.Resource{
		Name:   navIDShortlist,
		Title:  "Shortlist",
		Icon:   "⭐",
		Group:  grpHunt,
		Sort:   shortlistSpec,
		Filter: shortlistFilter,
		Perms:  resource.ReadAny,
		Lister: shortlistLister(store, adminUser, applicationsDir),
	}
}

// shortlistLister returns the go-panel resource Lister for /admin/shortlist.
//
// It delegates to Store.ListShortlist so the live code path and the isolation
// unit test exercise the same query — no decoy gap. PDF-derived filter chips
// (pack-ready, with-docs) cannot be expressed as SQL; they surface as Docs
// badges per row instead.
func shortlistLister(store *hunt.Store, adminUser, applicationsDir string) func(context.Context, resource.ListQuery) ([]resource.Row, int, error) {
	return func(ctx context.Context, q resource.ListQuery) ([]resource.Row, int, error) {
		storeRows, total, err := store.ListShortlist(ctx, hunt.ShortlistQuery{
			User:       adminUser,
			Stages:     shortlistActiveStages,
			WhereConds: q.WhereConds,
			WhereArgs:  q.WhereArgs,
			OrderBy:    shortlistSpec.OrderBy(q.Sort),
			Limit:      q.Limit,
			Offset:     q.Offset,
		})
		if err != nil {
			return nil, 0, err
		}

		// FIX 4: one ReadDir snapshot for the whole result set — avoids N+1 syscalls.
		// If the dir is unset or unreadable, rows appear with no Docs badges; the
		// lister never fails for a missing or empty applicationsDir.
		var appEntries []os.DirEntry
		if applicationsDir != "" {
			appEntries, _ = os.ReadDir(applicationsDir)
		}

		out := make([]resource.Row, 0, len(storeRows))
		for _, row := range storeRows {
			var hasResume, hasCover bool
			if len(appEntries) > 0 {
				if slug, slugErr := findApplicationSlugFromEntries(appEntries, row.Company, row.Title); slugErr == nil {
					slugDir := filepath.Join(applicationsDir, slug)
					hasResume = findApplicationPDF(slugDir, "resume") != ""
					hasCover = findApplicationPDF(slugDir, "cover") != ""
				}
			}

			titleCompany := row.Title
			if row.Company != "" {
				titleCompany = row.Title + " · " + row.Company
			}

			// Cell order MUST match shortlistSpec.Columns order.
			// Cell-0 = plain text (go-panel wraps in <a href>; cell.HTML ignored at i=0).
			out = append(out, resource.Row{
				ID:   strconv.FormatInt(row.ID, 10),
				Href: "/admin/jobs/" + strconv.FormatInt(row.ID, 10),
				Cells: []resource.Cell{
					{Value: titleCompany},                                                                         // [0] Title · Company
					{Value: stageBadgeHTML(row.Stage), HTML: true},                                               // [1] Stage
					{Value: fitChipHTML(row.FitScore, row.FitBand), HTML: true},                                  // [2] Fit
					{Value: marketReadHTML(row.SuccessBand, row.OverUnder), HTML: true},                          // [3] Market
					{Value: salaryDetailStr(row.SalaryMin, row.SalaryMax, row.SalaryCurrency, row.SalaryInterval)}, // [4] Comp
					{Value: docsChipHTML(hasResume, hasCover), HTML: true},                                       // [5] Docs
					{Value: row.RatedAt.Format("2006-01-02")},                                                    // [6] Rated
				},
			})
		}
		return out, total, nil
	}
}

// stageBadgeClass maps hunt stage constants to go-panel badge CSS modifier classes.
// Only values in this closed-enum map are used as CSS class names — unknown stages
// fall back to no modifier (plain .badge). No raw DB text appears in HTML attributes.
var stageBadgeClass = map[string]string{
	hunt.StageInteresting: cssBadgeBlue,
	hunt.StageSaved:       "",
	hunt.StageClaimed:     cssBadgeBlue,
	hunt.StageApplied:     cssBadgeBlue,
	hunt.StageInterview:   cssBadgeGreen,
	hunt.StageOffer:       cssBadgeGreen,
}

// stageBadgeHTML returns XSS-safe HTML for a stage badge cell.
// CSS class comes from the closed-enum stageBadgeClass map; stage text is escaped.
func stageBadgeHTML(stage string) string {
	cls := stageBadgeClass[stage] // "" for unknown / rejected / discarded stages
	extra := ""
	if cls != "" {
		extra = " " + cls
	}
	return fmt.Sprintf(`<span class="badge%s">%s</span>`, extra, html.EscapeString(stage))
}

// docsChipHTML returns XSS-safe HTML for the Docs cell.
// Input is fs-derived bool flags (not raw DB text) — closed-enum CSS only.
func docsChipHTML(hasResume, hasCover bool) string {
	switch {
	case hasResume && hasCover:
		return `<span class="badge badge-green">Pack-ready</span>`
	case hasResume:
		return `<span class="badge">Resume</span>`
	case hasCover:
		return `<span class="badge">Cover</span>`
	default:
		return `<span class="badge badge-gray">—</span>`
	}
}
