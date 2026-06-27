package adminui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/anatolykoptev/go-panel/resource"
	"github.com/anatolykoptev/go_job/internal/hunt"
)

// navIDShortlist is the sidebar nav id for the curated shortlist page.
const navIDShortlist = "shortlist"

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

// ── postgres path ──────────────────────────────────────────────────────────────

// pgShortlistEntry enriches a ShortlistRow with PDF-scan results and pre-rendered
// HTML chips so the template stays logic-free.
type pgShortlistEntry struct {
	hunt.ShortlistRow
	Slug        string
	HasResume   bool
	HasCover    bool
	PackReady   bool // HasResume && HasCover — derived at render
	FitChipHTML string
	MarketHTML  string
	CompDisplay string
	StageSlug   string // CSS-safe slug for the stage badge
}

type pgShortlistView struct {
	Total     int
	PackReady int
	WithDocs  int
	Saved     int // count of stage=="saved" entries for the filter chip label
	Filter    string
	Entries   []pgShortlistEntry
}

// enrichPGShortlist resolves PDF presence and pre-renders chip HTML for each row,
// then sorts pack-ready-first, then by fit_score desc (already from DB), then company.
func enrichPGShortlist(rows []hunt.ShortlistRow, applicationsDir string) []pgShortlistEntry {
	out := make([]pgShortlistEntry, 0, len(rows))
	for _, row := range rows {
		e := pgShortlistEntry{
			ShortlistRow: row,
			StageSlug:    slugify(row.Stage),
			FitChipHTML:  fitChipHTML(row.FitScore, row.FitBand),
			MarketHTML:   marketReadHTML(row.SuccessBand, row.OverUnder),
			CompDisplay:  salaryStr(row.SalaryMin, row.SalaryMax),
		}
		if e.CompDisplay != "" && row.SalaryCurrency != "" {
			e.CompDisplay += " " + row.SalaryCurrency
		}
		if slug, err := findApplicationSlug(applicationsDir, row.Company, row.Title); err == nil {
			e.Slug = slug
			slugDir := filepath.Join(applicationsDir, slug)
			e.HasResume = findApplicationPDF(slugDir, "resume") != ""
			e.HasCover = findApplicationPDF(slugDir, "cover") != ""
		}
		e.PackReady = e.HasResume && e.HasCover
		out = append(out, e)
	}
	// Sort: pack-ready first, then by fit_score desc (already ordered by DB),
	// then company. Use SliceStable to preserve DB fit_score order within same tier.
	sort.SliceStable(out, func(i, k int) bool {
		if out[i].PackReady != out[k].PackReady {
			return out[i].PackReady // pack-ready first
		}
		return false // preserve DB order (fit_score desc, company)
	})
	return out
}

// pgShortlistTmpl renders the postgres-backed shortlist. Fit/market chips are
// pre-rendered as HTML strings (FitChipHTML, MarketHTML) — no raw DB text in markup.
var pgShortlistTmpl = template.Must(template.New("pg_shortlist").Parse(`<style>
.sl-meta{color:var(--text-muted,#64748b);font-family:var(--font-mono,monospace);font-size:.75rem;margin-bottom:1rem}
.sl-badge{display:inline-block;padding:.1rem .45rem;border-radius:.25rem;font-size:.65rem;font-weight:600;text-transform:uppercase;letter-spacing:.04em}
.sl-interesting{background:#1e3a5f;color:#93c5fd}
.sl-saved{background:#1e293b;color:#94a3b8}
.sl-claimed{background:#14532d;color:#6ee7b7}
.sl-applied{background:#3b1c6b;color:#c4b5fd}
.sl-interview{background:#0d3a26;color:#6ee7b7}
.sl-offer{background:#14532d;color:#86efac}
.sl-rejected{background:#3b1c1c;color:#f87171}
.sl-comp{font-size:.78rem;color:#94a3b8}
.sl-dl{display:inline-block;margin-right:.4rem;padding:.2rem .55rem;background:#334155;color:#e2e8f0;border-radius:.3rem;text-decoration:none;font-size:.72rem}
.sl-dl:hover{background:#475569}
.sl-none{color:#475569;font-size:.75rem}
.sl-title a{color:#e2e8f0;text-decoration:none}
.sl-title a:hover{color:#93c5fd}
.sl-date{font-size:.75rem;color:#64748b;font-variant-numeric:tabular-nums}
</style>
<div class="page-header"><h2>Shortlist</h2><p>Curated target vacancies — rated jobs with resume + cover letter</p></div>
<div class="filter-bar">
  <a class="filter-chip{{if eq .Filter ""}} active{{end}}" href="/admin/shortlist">All {{.Total}}</a>
  <a class="filter-chip{{if eq .Filter "docs"}} active{{end}}" href="/admin/shortlist?status=docs">With docs {{.WithDocs}}</a>
  <a class="filter-chip{{if eq .Filter "pack-ready"}} active{{end}}" href="/admin/shortlist?status=pack-ready">Pack-ready {{.PackReady}}</a>
  <a class="filter-chip{{if eq .Filter "saved"}} active{{end}}" href="/admin/shortlist?status=saved">Saved {{.Saved}}</a>
</div>
<div class="sl-meta">source: hunt_jobs ⋈ hunt_ratings</div>
<table class="crm-table">
  <thead><tr><th>Company</th><th>Role</th><th>Fit</th><th>Market</th><th>Stage</th><th>Posted</th><th>Scored</th><th>Comp</th><th>Documents</th></tr></thead>
  <tbody>
  {{range .Entries}}
    <tr>
      <td class="row-name">{{.Company}}</td>
      <td class="sl-title">{{if .URL}}<a href="{{.URL}}" target="_blank" rel="noopener noreferrer">{{.Title}}</a>{{else}}{{.Title}}{{end}}<span class="row-sub">{{.Location}}</span></td>
      <td>{{.FitChipHTML}}</td>
      <td>{{.MarketHTML}}</td>
      <td><span class="sl-badge sl-{{.StageSlug}}">{{.Stage}}</span></td>
      <td class="sl-date">{{.PostedDisplay}}</td>
      <td class="sl-date">{{.ScoredDisplay}}</td>
      <td class="sl-comp">{{.CompDisplay}}</td>
      <td>
        {{if .HasResume}}<a class="sl-dl" href="/admin/shortlist/{{.Slug}}/download/resume">Resume PDF</a>{{end}}
        {{if .HasCover}}<a class="sl-dl" href="/admin/shortlist/{{.Slug}}/download/cover">Cover PDF</a>{{end}}
        {{if and (not .HasResume) (not .HasCover)}}<span class="sl-none">—</span>{{end}}
      </td>
    </tr>
  {{end}}
  </tbody>
</table>`))

// pgShortlistEntryVM wraps pgShortlistEntry with template-friendly display fields.
// html/template auto-escapes string fields, but FitChipHTML and MarketHTML are
// pre-escaped HTML — they must be template.HTML to avoid double-escaping.
type pgShortlistEntryVM struct {
	pgShortlistEntry
	FitChipHTML   template.HTML
	MarketHTML    template.HTML
	PostedDisplay string
	ScoredDisplay string
}

func renderPGShortlistHTML(entries []pgShortlistEntry, filter string) string {
	var packReady, withDocs, saved int
	for _, e := range entries {
		if e.PackReady {
			packReady++
		}
		if e.HasResume || e.HasCover {
			withDocs++
		}
		if e.Stage == hunt.StageSaved {
			saved++
		}
	}

	shown := entries
	switch filter {
	case "pack-ready":
		shown = nil
		for _, e := range entries {
			if e.PackReady {
				shown = append(shown, e)
			}
		}
	case "saved":
		shown = nil
		for _, e := range entries {
			if e.Stage == hunt.StageSaved {
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

	// Build VM slice with template.HTML chip fields and date strings.
	vms := make([]pgShortlistEntryVM, 0, len(shown))
	for _, e := range shown {
		vm := pgShortlistEntryVM{
			pgShortlistEntry: e,
			FitChipHTML:      template.HTML(e.FitChipHTML), //nolint:gosec // pre-rendered by fitChipHTML (closed-enum CSS only)
			MarketHTML:       template.HTML(e.MarketHTML),   //nolint:gosec // pre-rendered by marketReadHTML (closed-enum CSS only)
			PostedDisplay:    dateStr(e.PostedAt),
			ScoredDisplay:    dateStr(e.ScoredAt),
		}
		vms = append(vms, vm)
	}

	vm := pgShortlistView{
		Total:     len(entries),
		PackReady: packReady,
		WithDocs:  withDocs,
		Saved:     saved,
		Filter:    filter,
		Entries:   nil, // not used — vms passed separately
	}

	type tplData struct {
		pgShortlistView
		Entries []pgShortlistEntryVM
	}
	data := tplData{pgShortlistView: vm, Entries: vms}

	var buf bytes.Buffer
	if err := pgShortlistTmpl.Execute(&buf, data); err != nil {
		return `<div class="page-header"><h2>Shortlist</h2><p>render error</p></div>`
	}
	return buf.String()
}

func shortlistPGEmptyHTML() string {
	return strings.Join([]string{
		`<div class="page-header"><h2>Shortlist</h2></div>`,
		`<div class="sl-meta">No rated jobs found — use the Rate form on a job detail page`,
		` or run the migrate-tracker command to seed from _tracker.json.</div>`,
	}, "")
}

// ── JSON / _tracker.json path (rollback lever, kept through Phase 3) ───────────
//
// trackerFile mirrors applications/_tracker.json — the operator's curated
// shortlist of target vacancies (the "favorites"). Retained for rollback:
// set SHORTLIST_SOURCE=json to revert to this path. Removed in Phase 3.

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
	case "pack-ready":
		return 0
	case "saved":
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
		case "pack-ready":
			packReady++
		case "saved":
			saved++
		}
		if e.HasResume || e.HasCover {
			withDocs++
		}
	}
	shown := entries
	switch filter {
	case "pack-ready", "saved":
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

func shortlistEmptyHTML() string {
	return `<div class="page-header"><h2>Shortlist</h2></div><div class="sl-meta">No shortlist found — _tracker.json is missing or unreadable.</div>`
}

// ── handler ────────────────────────────────────────────────────────────────────

// shortlistHandler renders the curated favorites page. The data source is
// controlled by the SHORTLIST_SOURCE env var (read at request time for hot-flip):
//   - "" or "pg" (default): reads hunt_jobs JOIN hunt_ratings (postgres path)
//   - "json": reads _tracker.json (rollback lever, removed in Phase 3)
//
// The download sub-routes (/admin/shortlist/{slug}/download/{kind}) are unchanged
// and always use the filesystem scan.
func shortlistHandler(p *resource.Panel, store *hunt.Store, adminUser, applicationsDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		filter := r.URL.Query().Get("status")

		// SHORTLIST_SOURCE=json → rollback to _tracker.json reader.
		if os.Getenv("SHORTLIST_SOURCE") == "json" {
			tf, err := loadTracker(applicationsDir)
			if err != nil {
				slog.WarnContext(r.Context(), "shortlist: load tracker (json fallback)", "err", err)
				_ = p.RenderPageHTML(w, r, "Shortlist", navIDShortlist, shortlistEmptyHTML())
				return
			}
			entries := enrichShortlist(tf.Jobs, applicationsDir)
			_ = p.RenderPageHTML(w, r, "Shortlist", navIDShortlist, renderShortlistHTML(tf, entries, filter))
			return
		}

		// Default postgres path.
		rows, err := store.ListShortlist(r.Context(), adminUser, shortlistActiveStages)
		if err != nil {
			slog.WarnContext(r.Context(), "shortlist: list from postgres", "err", err)
			_ = p.RenderPageHTML(w, r, "Shortlist", navIDShortlist, shortlistPGEmptyHTML())
			return
		}
		if len(rows) == 0 {
			_ = p.RenderPageHTML(w, r, "Shortlist", navIDShortlist, shortlistPGEmptyHTML())
			return
		}
		entries := enrichPGShortlist(rows, applicationsDir)
		_ = p.RenderPageHTML(w, r, "Shortlist", navIDShortlist, renderPGShortlistHTML(entries, filter))
	}
}

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
			slog.Error("shortlistDownload: path traversal", "path", pdfPath, "root", applicationsDir)
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
