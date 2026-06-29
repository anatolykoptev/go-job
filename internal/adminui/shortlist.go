package adminui

import (
	"context"
	"fmt"
	"html"
	"strconv"
	"time"

	"github.com/anatolykoptev/go-kit/admintable"
	"github.com/anatolykoptev/go-panel/resource"
	"github.com/anatolykoptev/go-panel/shell"
	"github.com/anatolykoptev/go_job/internal/engine/jobs/applications"
	"github.com/anatolykoptev/go_job/internal/hunt"
)

// navIDShortlist is the sidebar nav ID for the curated shortlist page.
// It matches resource.Resource.Name so go-panel auto-routes /admin/shortlist.
const navIDShortlist = "shortlist"

// Badge CSS modifier classes (goconst threshold ≥4 across stageBadgeClass + tests).
const (
	cssBadgeBlue  = "badge-blue"
	cssBadgeGreen = "badge-green"
	cssBadgeGray  = "badge-gray" // used for discarded (negative triage decision)
)

// shortlistTriageValues is the set of hunt_ratings.triage values that bring a job
// onto the shortlist. Excludes discarded (deliberate negative triage decision).
// Shared with jobs.go (star computation) and star.go (toggle params).
var shortlistTriageValues = []string{
	hunt.StageInteresting,
	hunt.StageSaved,
}

// shortlistPipelineValues is the set of hunt_ratings.stage values that bring a job
// onto the shortlist (i.e. the operator has actively moved it into the pipeline).
// Excludes rejected (terminal-negative pipeline state).
// Shared with jobs.go (star computation) and star.go (toggle params).
var shortlistPipelineValues = []string{
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
		{Key: colKeyTitle, Label: lblTitle, Sortable: true, SQLExpr: sqlJTitle},
		// Star column at index 1 (front after Title). Every shortlist row is starred
		// (stage ∈ shortlistActiveStages by query). Clicking demotes to StageNew →
		// row drops off shortlist on reload.
		{Key: colKeyStar, Label: "★", Sortable: false, Width: "3rem"},
		{Key: colCompany, Label: "Company", Sortable: true, SQLExpr: sqlJCompany},
		// "Triage / Stage" renders triage + pipeline badges together (triageStageBadgesHTML).
		// Sort applies to the pipeline axis (r.stage) only; triage has no separate sort key.
		{Key: colKeyStage, Label: "Triage / Stage", Sortable: true, SQLExpr: sqlRStage, Width: colWidth8rem},
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
// Two separate axis filters: triage (interest signal) and stage (pipeline position).
// PDF-derived filters (pack-ready, with-docs) cannot be expressed as SQL — they
// are surfaced as badges in the Docs cell instead.
var shortlistFilter = admintable.FilterSpec{Filters: []admintable.Filter{
	{Key: keyQ, SQLExprs: []string{sqlJTitle, sqlJCompany}, Match: admintable.ILike},
	{Key: colKeyTriage, SQLExpr: sqlRTriage, Match: admintable.Eq, Allowed: hunt.TriageStages},
	{Key: colKeyStage, SQLExpr: sqlRStage, Match: admintable.Eq, Allowed: hunt.PipelineStages},
}}

func shortlistResource(store *hunt.Store, adminUser string, authority *applications.Authority, csrfKey []byte) resource.Resource {
	return resource.Resource{
		Name:  navIDShortlist,
		Title: "Shortlist",
		Icon:  "★",
		Group: grpHunt,
		Sort:  shortlistSpec,
		Filter: shortlistFilter,
		Perms: resource.ReadAny,
		Badge: shell.CachedBadge(30*time.Second, func(ctx context.Context) string {
			n := store.CountShortlist(ctx, adminUser, shortlistTriageValues, shortlistPipelineValues)
			if n == 0 {
				return ""
			}
			return strconv.Itoa(n)
		}),
		Lister: shortlistLister(store, adminUser, authority, csrfKey),
	}
}

// shortlistLister returns the go-panel resource Lister for /admin/shortlist.
//
// It delegates to Store.ListShortlist so the live code path and the isolation
// unit test exercise the same query — no decoy gap. PDF-derived filter chips
// (pack-ready, with-docs) cannot be expressed as SQL; they surface as Docs
// badges per row instead.
func shortlistLister(store *hunt.Store, adminUser string, authority *applications.Authority, csrfKey []byte) func(context.Context, resource.ListQuery) ([]resource.Row, int, error) {
	return func(ctx context.Context, q resource.ListQuery) ([]resource.Row, int, error) {
		storeRows, total, err := store.ListShortlist(ctx, hunt.ShortlistQuery{
			User:         adminUser,
			TriageValues: shortlistTriageValues,
			StageValues:  shortlistPipelineValues,
			WhereConds:   q.WhereConds,
			WhereArgs:    q.WhereArgs,
			OrderBy:      shortlistSpec.OrderBy(q.Sort),
			Limit:        q.Limit,
			Offset:       q.Offset,
		})
		if err != nil {
			return nil, 0, err
		}

		// One legacy-dir snapshot for the whole result set — avoids N+1 ReadDir syscalls.
		// Authority.LegacyEntries returns nil when legacyDir is empty or unreadable;
		// rows then show no Docs badges without failing.
		legacyEntries := authority.LegacyEntries()

		// Mint a single CSRF token for all star-toggle forms on this page.
		csrfTok := mintStarCSRF(ctx, csrfKey)

		out := make([]resource.Row, 0, len(storeRows))
		for _, row := range storeRows {
			// Uploads-first: canonical path per hunt_jobs.id.
			hasResume := authority.Exists(row.ID, applications.KindResume)
			hasCover := authority.Exists(row.ID, applications.KindCover)
			// Legacy fallback: fuzzy slug under APPLICATIONS_DIR.
			if !hasResume {
				hasResume = authority.LegacyExistsFromEntries(legacyEntries, row.Company, row.Title, applications.KindResume)
			}
			if !hasCover {
				hasCover = authority.LegacyExistsFromEntries(legacyEntries, row.Company, row.Title, applications.KindCover)
			}

			// Cell order MUST match shortlistSpec.Columns order.
			// Cell-0 = plain text Title (go-panel wraps in <a href>; cell.HTML ignored
			// at i=0). Star is at cell-1 (front after Title). Company at cell-2.
			// Every shortlist row is starred (true) — it's on the list by query definition.
			out = append(out, resource.Row{
				ID:   strconv.FormatInt(row.ID, 10),
				Href: "/admin/jobs/" + strconv.FormatInt(row.ID, 10),
				Cells: []resource.Cell{
					{Value: row.Title},                                                                              // [0] Title (plain text — Href-linked)
					{Value: starToggleHTML(row.ID, true, csrfTok), HTML: true},                                     // [1] Star (front after Title; always ★)
					{Value: row.Company},                                                                            // [2] Company
					{Value: triageStageBadgesHTML(row.Triage, row.Stage), HTML: true},                              // [3] Triage + Stage badges
					{Value: fitChipHTML(row.FitScore, row.FitBand), HTML: true},                                    // [4] Fit
					{Value: marketReadHTML(row.SuccessBand, row.OverUnder), HTML: true},                            // [5] Market
					{Value: salaryDetailStr(row.SalaryMin, row.SalaryMax, row.SalaryCurrency, row.SalaryInterval)}, // [6] Comp
					{Value: docsChipHTML(row.ID, hasResume, hasCover), HTML: true},                                 // [7] Docs
					{Value: row.RatedAt.Format("2006-01-02")},                                                      // [8] Rated
				},
			})
		}
		return out, total, nil
	}
}

// stageBadgeClass maps hunt stage/triage constants to go-panel badge CSS modifier classes.
// Only values in this closed-enum map are used as CSS class names — unknown values
// fall back to no modifier (plain .badge). No raw DB text appears in HTML attributes.
var stageBadgeClass = map[string]string{
	// Triage axis
	hunt.StageInteresting: cssBadgeBlue,
	hunt.StageSaved:       "",           // default badge — neutral/selected
	hunt.StageDiscarded:   cssBadgeGray, // muted — negative decision, visually distinct from saved
	// Pipeline axis
	hunt.StageClaimed:   cssBadgeBlue,
	hunt.StageApplied:   cssBadgeBlue,
	hunt.StageInterview: cssBadgeGreen,
	hunt.StageOffer:     cssBadgeGreen,
}

// stageBadgeHTML returns XSS-safe HTML for a single stage/triage badge.
// CSS class comes from the closed-enum stageBadgeClass map; text is escaped.
// Returns empty string when value is "".
func stageBadgeHTML(value string) string {
	if value == "" {
		return ""
	}
	cls := stageBadgeClass[value] // "" for unknown / rejected
	extra := ""
	if cls != "" {
		extra = " " + cls
	}
	return fmt.Sprintf(`<span class="badge%s">%s</span>`, extra, html.EscapeString(value))
}

// triageStageBadgesHTML renders up to two badges — one for triage, one for pipeline stage.
// Either may be empty (""); only non-empty values are rendered. Used in the shortlist
// Stage column (post-migration-012 split).
func triageStageBadgesHTML(triage, stage string) string {
	t := stageBadgeHTML(triage)
	s := stageBadgeHTML(stage)
	switch {
	case t == "" && s == "":
		return `<span class="badge badge-gray">—</span>`
	case t != "" && s != "":
		return t + " " + s
	case t != "":
		return t
	default:
		return s
	}
}

// docsChipHTML returns XSS-safe HTML for the Docs cell.
// When a PDF exists it renders a clickable download link pointing at the existing
// GET /admin/jobs/{id}/download/{kind} route (downloadHandler). id is an int64
// primary key — no user text, no escaping required. CSS class badge-green is a
// go-panel built-in available on all admin pages.
func docsChipHTML(id int64, hasResume, hasCover bool) string {
	if !hasResume && !hasCover {
		return `<span class="badge badge-gray">—</span>`
	}
	base := "/admin/jobs/" + strconv.FormatInt(id, 10) + "/download/"
	out := ""
	if hasResume {
		out += `<a class="badge badge-green" href="` + base + `resume">Resume</a>`
	}
	if hasResume && hasCover {
		out += " "
	}
	if hasCover {
		out += `<a class="badge badge-green" href="` + base + `cover">Cover</a>`
	}
	return out
}
