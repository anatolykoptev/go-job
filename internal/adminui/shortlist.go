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

	"github.com/anatolykoptev/go-panel/resource"
)

// navIDShortlist is the sidebar nav id for the curated shortlist page.
const navIDShortlist = "shortlist"

// trackerFile mirrors applications/_tracker.json — the operator's curated
// shortlist of target vacancies (the "favorites"). It is the live source of
// truth (not hunt_jobs.recommendation_tier, which is unused).
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

// shortlistHandler renders the curated favorites page from _tracker.json, each
// entry enriched with resume/cover PDF links (shown only when the file exists).
func shortlistHandler(p *resource.Panel, applicationsDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tf, err := loadTracker(applicationsDir)
		if err != nil {
			slog.WarnContext(r.Context(), "shortlist: load tracker", "err", err)
			_ = p.RenderPageHTML(w, r, "Shortlist", navIDShortlist, shortlistEmptyHTML())
			return
		}
		entries := enrichShortlist(tf.Jobs, applicationsDir)
		filter := r.URL.Query().Get("status")
		_ = p.RenderPageHTML(w, r, "Shortlist", navIDShortlist, renderShortlistHTML(tf, entries, filter))
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
