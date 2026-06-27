package adminui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/anatolykoptev/go-kit/admintable"
	"github.com/anatolykoptev/go-panel/resource"
	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/jackc/pgx/v5/pgxpool"
)

// navIDShortlist is the sidebar nav ID for the curated shortlist page.
// It matches resource.Resource.Name so go-panel auto-routes /admin/shortlist.
const navIDShortlist = "shortlist"

// Badge CSS modifier classes (goconst threshold ≥4 across stageBadgeClass + tests).
const (
	cssBadgeBlue  = "badge-blue"
	cssBadgeGreen = "badge-green"
)

// statusPackReady is the JSON-path tracker status value for pack-ready entries.
// Kept through Phase 3 alongside the JSON fallback path.
const statusPackReady = "pack-ready"

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
// are shown as badges in the Docs cell instead. Stage and text-search filters are
// SQL-backed via the FilterSpec.
var shortlistFilter = admintable.FilterSpec{Filters: []admintable.Filter{
	{Key: keyQ, SQLExprs: []string{"j.title", "j.company"}, Match: admintable.ILike},
	{Key: "stage", SQLExpr: "r.stage", Match: admintable.Eq, Allowed: shortlistActiveStages},
}}

func shortlistResource(pool *pgxpool.Pool, adminUser, applicationsDir string) resource.Resource {
	return resource.Resource{
		Name:   navIDShortlist,
		Title:  "Shortlist",
		Icon:   "⭐",
		Group:  grpHunt,
		Sort:   shortlistSpec,
		Filter: shortlistFilter,
		Perms:  resource.ReadAny,
		Lister: shortlistLister(pool, adminUser, applicationsDir),
	}
}

// shortlistJoinFrom is the FROM + JOIN clause shared between the COUNT and row queries.
const shortlistJoinFrom = `FROM hunt_jobs j JOIN hunt_ratings r ON r.entry_kind='job' AND r.entry_id=j.id`

// shortlistLister returns the go-panel resource Lister for /admin/shortlist.
// It joins hunt_jobs + hunt_ratings, restricts to adminUser + shortlistActiveStages,
// scans application-dir PDFs per row for Docs badges, and paginates via LIMIT/OFFSET.
//
// PDF-derived filter chips (pack-ready, with-docs) are intentionally omitted from
// shortlistFilter because they require filesystem state that cannot be expressed as
// SQL. PDF status is surfaced as a Docs badge in every row.
func shortlistLister(pool *pgxpool.Pool, adminUser, applicationsDir string) func(context.Context, resource.ListQuery) ([]resource.Row, int, error) {
	return func(ctx context.Context, q resource.ListQuery) ([]resource.Row, int, error) {
		where := "TRUE"
		if strings.TrimSpace(q.WhereConds) != "" {
			where = q.WhereConds
		}
		// Arg indices: $1…$N = q.WhereArgs (from FilterSpec), $N+1 = adminUser,
		// $N+2 = shortlistActiveStages array, $N+3 = LIMIT, $N+4 = OFFSET.
		n := len(q.WhereArgs)
		//nolint:gosec // fullWhere = q.WhereConds (author-controlled FilterSpec SQLExpr/SQLExprs + literal operators) + literal "AND r.user_name = $N ... r.stage = ANY($N...)"; all URL values are bind args.
		fullWhere := fmt.Sprintf(
			"%s AND r.user_name = $%d AND r.stage = ANY($%d::text[])",
			where, n+1, n+2,
		)
		baseArgs := append(append([]any{}, q.WhereArgs...), adminUser, shortlistActiveStages)

		var total int
		if err := pool.QueryRow(ctx,
			"SELECT count(*) "+shortlistJoinFrom+" WHERE "+fullWhere,
			baseArgs...,
		).Scan(&total); err != nil {
			return nil, 0, fmt.Errorf("adminui: count shortlist: %w", err)
		}

		queryArgs := append(append([]any{}, baseArgs...), q.Limit, q.Offset)
		//nolint:gosec // shortlistSpec.OrderBy returns author-declared SQLExpr + literal ASC/DESC/NULLS LAST; no URL input interpolated.
		query := fmt.Sprintf(`
			SELECT j.id, COALESCE(j.title,''), COALESCE(j.company,''), COALESCE(j.url,''),
			       COALESCE(j.location,''), j.fit_score, COALESCE(j.fit_band,''),
			       COALESCE(j.success_band,''), COALESCE(j.over_under,''),
			       j.salary_min, j.salary_max, COALESCE(j.salary_currency,''), COALESCE(j.salary_interval,''),
			       j.posted_at, j.scored_at, r.stage, r.rated_at
			%s WHERE %s ORDER BY %s LIMIT $%d OFFSET $%d`,
			shortlistJoinFrom, fullWhere, shortlistSpec.OrderBy(q.Sort), n+3, n+4)

		rows, err := pool.Query(ctx, query, queryArgs...)
		if err != nil {
			return nil, 0, fmt.Errorf("adminui: list shortlist: %w", err)
		}
		defer rows.Close()

		var out []resource.Row
		for rows.Next() {
			var (
				id                            int64
				title, company, url, location string
				fitBand, sucBand, ou          string
				currency, interval            string
				stage                         string
				fit                           *int
				salMin, salMax                *int
				postedAt, scoredAt            *time.Time
				ratedAt                       time.Time
			)
			if err := rows.Scan(
				&id, &title, &company, &url, &location,
				&fit, &fitBand, &sucBand, &ou,
				&salMin, &salMax, &currency, &interval,
				&postedAt, &scoredAt, &stage, &ratedAt,
			); err != nil {
				return nil, 0, fmt.Errorf("adminui: scan shortlist: %w", err)
			}

			titleCompany := title
			if company != "" {
				titleCompany = title + " · " + company
			}

			// PDF scan (filesystem). Fast for ~30 curated rows per request.
			var hasResume, hasCover bool
			if slug, slugErr := findApplicationSlug(applicationsDir, company, title); slugErr == nil {
				slugDir := filepath.Join(applicationsDir, slug)
				hasResume = findApplicationPDF(slugDir, "resume") != ""
				hasCover = findApplicationPDF(slugDir, "cover") != ""
			}

			// Cell order MUST match shortlistSpec.Columns order.
			// Cell-0 = plain text (go-panel wraps in <a href>; cell.HTML ignored at i=0).
			out = append(out, resource.Row{
				ID:   strconv.FormatInt(id, 10),
				Href: "/admin/jobs/" + strconv.FormatInt(id, 10),
				Cells: []resource.Cell{
					{Value: titleCompany},                                             // [0] Title · Company
					{Value: stageBadgeHTML(stage), HTML: true},                        // [1] Stage
					{Value: fitChipHTML(fit, fitBand), HTML: true},                    // [2] Fit
					{Value: marketReadHTML(sucBand, ou), HTML: true},                  // [3] Market
					{Value: salaryDetailStr(salMin, salMax, currency, interval)},      // [4] Comp
					{Value: docsChipHTML(hasResume, hasCover), HTML: true},            // [5] Docs
					{Value: ratedAt.Format("2006-01-02")},                             // [6] Rated
				},
			})
		}
		return out, total, rows.Err()
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

// ── JSON / _tracker.json path (rollback lever, kept through Phase 3) ───────────
//
// trackerFile mirrors applications/_tracker.json — the operator's curated
// shortlist of target vacancies (the "favorites"). Retained for rollback.
// Removed in Phase 3 alongside the SHORTLIST_SOURCE env var.

type trackerFile struct {
	Version int          `json:"version"`
	Updated string       `json:"updated"`
	Jobs    []trackerJob `json:"jobs"`
}

type trackerJob struct {
	Score      int    `json:"score"`
	Company    string `json:"company"`
	Title      string `json:"title"`
	Location   string `json:"location"`
	URL        string `json:"url"`
	Comp       string `json:"comp"`
	Department string `json:"department"`
	Status     string `json:"status"`
	Added      string `json:"added"`
}

// shortlistEntry enriches a tracker job with its application-dir slug and the
// availability of prepared resume/cover PDFs.
type shortlistEntry struct {
	trackerJob
	Slug       string
	StatusSlug string
	HasResume  bool
	HasCover   bool
}

type shortlistView struct {
	Updated   string
	Total     int
	PackReady int
	Saved     int
	WithDocs  int
	Filter    string
	Entries   []shortlistEntry
}

// loadTracker reads and parses <applicationsDir>/_tracker.json.
func loadTracker(applicationsDir string) (*trackerFile, error) {
	raw, err := os.ReadFile(filepath.Join(applicationsDir, "_tracker.json"))
	if err != nil {
		return nil, err
	}
	var tf trackerFile
	if err := json.Unmarshal(raw, &tf); err != nil {
		return nil, fmt.Errorf("parse _tracker.json: %w", err)
	}
	return &tf, nil
}

// statusRank orders entries: pack-ready (prepared) first, then saved, then rest.
func statusRank(s string) int {
	switch s {
	case statusPackReady:
		return 0
	case hunt.StageSaved:
		return 1
	default:
		return 2
	}
}

// enrichShortlist resolves each tracker job's application dir + PDF availability,
// then sorts pack-ready first, then by score desc, then company.
func enrichShortlist(jobs []trackerJob, applicationsDir string) []shortlistEntry {
	out := make([]shortlistEntry, 0, len(jobs))
	for _, j := range jobs {
		e := shortlistEntry{trackerJob: j, StatusSlug: slugify(j.Status)}
		if slug, err := findApplicationSlug(applicationsDir, j.Company, j.Title); err == nil {
			e.Slug = slug
			slugDir := filepath.Join(applicationsDir, slug)
			e.HasResume = findApplicationPDF(slugDir, "resume") != ""
			e.HasCover = findApplicationPDF(slugDir, "cover") != ""
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, k int) bool {
		ri, rk := statusRank(out[i].Status), statusRank(out[k].Status)
		if ri != rk {
			return ri < rk
		}
		if out[i].Score != out[k].Score {
			return out[i].Score > out[k].Score
		}
		return out[i].Company < out[k].Company
	})
	return out
}

var shortlistTmpl = template.Must(template.New("shortlist").Parse(`<style>
.sl-meta{color:var(--text-muted,#64748b);font-family:var(--font-mono,monospace);font-size:.75rem;margin-bottom:1rem}
.sl-badge{display:inline-block;padding:.1rem .45rem;border-radius:.25rem;font-size:.65rem;font-weight:600;text-transform:uppercase;letter-spacing:.04em}
.sl-pack-ready{background:#0d3a26;color:#6ee7b7}
.sl-saved{background:#1e293b;color:#94a3b8}
.sl-score{font-variant-numeric:tabular-nums;font-weight:600;color:#93c5fd}
.sl-comp{font-size:.78rem;color:#94a3b8}
.sl-dl{display:inline-block;margin-right:.4rem;padding:.2rem .55rem;background:#334155;color:#e2e8f0;border-radius:.3rem;text-decoration:none;font-size:.72rem}
.sl-dl:hover{background:#475569}
.sl-none{color:#475569;font-size:.75rem}
.sl-title a{color:#e2e8f0;text-decoration:none}
.sl-title a:hover{color:#93c5fd}
</style>
<div class="page-header"><h2>Shortlist</h2><p>Curated target vacancies with prepared resume + cover letter</p></div>
<div class="filter-bar">
  <a class="filter-chip{{if eq .Filter ""}} active{{end}}" href="/admin/shortlist">All {{.Total}}</a>
  <a class="filter-chip{{if eq .Filter "docs"}} active{{end}}" href="/admin/shortlist?status=docs">With docs {{.WithDocs}}</a>
  <a class="filter-chip{{if eq .Filter "pack-ready"}} active{{end}}" href="/admin/shortlist?status=pack-ready">Pack-ready {{.PackReady}}</a>
  <a class="filter-chip{{if eq .Filter "saved"}} active{{end}}" href="/admin/shortlist?status=saved">Saved {{.Saved}}</a>
</div>
<div class="sl-meta">updated {{.Updated}}</div>
<table class="crm-table">
  <thead><tr><th>Company</th><th>Role</th><th>Score</th><th>Status</th><th>Comp</th><th>Documents</th></tr></thead>
  <tbody>
  {{range .Entries}}
    <tr>
      <td class="row-name">{{.Company}}</td>
      <td class="sl-title">{{if .URL}}<a href="{{.URL}}" target="_blank" rel="noopener noreferrer">{{.Title}}</a>{{else}}{{.Title}}{{end}}<span class="row-sub">{{.Location}}</span></td>
      <td class="sl-score">{{.Score}}</td>
      <td><span class="sl-badge sl-{{.StatusSlug}}">{{.Status}}</span></td>
      <td class="sl-comp">{{.Comp}}</td>
      <td>
        {{if .HasResume}}<a class="sl-dl" href="/admin/shortlist/{{.Slug}}/download/resume">Resume PDF</a>{{end}}
        {{if .HasCover}}<a class="sl-dl" href="/admin/shortlist/{{.Slug}}/download/cover">Cover PDF</a>{{end}}
        {{if and (not .HasResume) (not .HasCover)}}<span class="sl-none">—</span>{{end}}
      </td>
    </tr>
  {{end}}
  </tbody>
</table>`))

func renderShortlistHTML(tf *trackerFile, entries []shortlistEntry, filter string) string {
	var packReady, saved, withDocs int
	for _, e := range entries {
		switch e.Status {
		case statusPackReady:
			packReady++
		case hunt.StageSaved:
			saved++
		}
		if e.HasResume || e.HasCover {
			withDocs++
		}
	}
	shown := entries
	switch filter {
	case statusPackReady, hunt.StageSaved:
		shown = nil
		for _, e := range entries {
			if e.Status == filter {
				shown = append(shown, e)
			}
		}
	case "docs":
		shown = nil
		for _, e := range entries {
			if e.HasResume || e.HasCover {
				shown = append(shown, e)
			}
		}
	}
	vm := shortlistView{
		Updated:   tf.Updated,
		Total:     len(entries),
		PackReady: packReady,
		Saved:     saved,
		WithDocs:  withDocs,
		Filter:    filter,
		Entries:   shown,
	}
	var buf bytes.Buffer
	if err := shortlistTmpl.Execute(&buf, vm); err != nil {
		return `<div class="page-header"><h2>Shortlist</h2><p>render error</p></div>`
	}
	return buf.String()
}

// ── handler ────────────────────────────────────────────────────────────────────

var safeSlugRe = regexp.MustCompile(`^[a-z0-9-]+$`)

// shortlistDownloadHandler serves a resume/cover PDF for a tracker entry by its
// application-dir slug (the shortlist has no hunt_jobs id). Slug is validated as
// a simple slug and the resolved path is confined under applicationsDir.
func shortlistDownloadHandler(applicationsDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("slug")
		kind := r.PathValue("kind")
		if !validDownloadKinds[kind] {
			http.Error(w, "invalid kind; must be resume or cover", http.StatusBadRequest)
			return
		}
		if slug == "" || !safeSlugRe.MatchString(slug) {
			http.Error(w, "bad slug", http.StatusBadRequest)
			return
		}
		slugDir := filepath.Join(applicationsDir, slug)
		pdfPath := findApplicationPDF(slugDir, kind)
		if pdfPath == "" {
			http.Error(w, kind+" PDF not found", http.StatusNotFound)
			return
		}
		if !ValidatePathUnderRoot(applicationsDir, pdfPath) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		abs, err := filepath.Abs(pdfPath)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, filepath.Base(abs)))
		http.ServeFile(w, r, abs)
	}
}
