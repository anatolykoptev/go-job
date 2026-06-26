package adminui

import (
	"bytes"
	"html/template"
	"log/slog"
	"net/http"

	"github.com/anatolykoptev/go-panel/resource"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
)

// resumeHandler returns an http.HandlerFunc that renders the operator's
// full resume profile from PostgreSQL inside the go-panel shell chrome.
//
// Empty-state: when no resume data exists (no person row), renders a
// friendly message instead of a 500.
func resumeHandler(p *resource.Panel) http.HandlerFunc {
	tmpl := template.Must(template.New("resume").Funcs(template.FuncMap{
		"join":            resumeJoin,
		"skillCategories": resumeSkillCategories,
	}).Parse(resumeTmplSrc))

	return func(w http.ResponseWriter, r *http.Request) {
		db := jobs.GetResumeDB()
		if db == nil {
			renderShell(w, r, p, "Resume", "resume", resumeEmptyHTML("Resume database not configured (set DATABASE_URL)."))
			return
		}

		personID := db.GetLatestPersonID(r.Context())
		if personID == 0 {
			renderShell(w, r, p, "Resume", "resume", resumeEmptyHTML("No resume data yet — run master_resume_build first."))
			return
		}

		// GetResumeProfile handles all section loading; section="" = full profile.
		profile, err := jobs.GetResumeProfile(r.Context(), "")
		if err != nil {
			slog.Warn("resumeHandler: GetResumeProfile", "err", err)
			renderShell(w, r, p, "Resume", "resume", resumeEmptyHTML("Could not load resume: "+err.Error()))
			return
		}

		var buf bytes.Buffer
		if execErr := tmpl.Execute(&buf, profile); execErr != nil {
			slog.Error("resumeHandler: template execute", "err", execErr)
			http.Error(w, "render error", http.StatusInternalServerError)
			return
		}
		renderShell(w, r, p, "Resume", "resume", buf.String())
	}
}

// resumeEmptyHTML returns a simple message fragment for the empty/error state.
func resumeEmptyHTML(msg string) string {
	return `<div style="padding:2rem 1.5rem;color:#94a3b8;font-family:system-ui,sans-serif">` +
		template.HTMLEscapeString(msg) +
		`</div>`
}

// resumeJoin joins a slice of strings with the given separator.
func resumeJoin(ss []string, sep string) string {
	var buf bytes.Buffer
	for i, s := range ss {
		if i > 0 {
			buf.WriteString(sep)
		}
		buf.WriteString(s)
	}
	return buf.String()
}

// resumeSkillCategories returns deduplicated ordered categories from skills.
func resumeSkillCategories(skills []jobs.SkillSummary) []string {
	seen := make(map[string]bool)
	var out []string
	for _, s := range skills {
		if !seen[s.Category] {
			seen[s.Category] = true
			out = append(out, s.Category)
		}
	}
	return out
}

// resumeTmplSrc is the HTML content fragment for the resume profile page.
// Embedded inside go-panel shell.Layout via renderShell.
const resumeTmplSrc = `<style>
.rp{max-width:860px;margin:0 auto;padding:2rem 1.5rem;font-family:system-ui,sans-serif;color:#e2e8f0}
.rp-header{margin-bottom:1.5rem}
.rp-header h2{margin:0 0 .25rem;font-size:1.5rem;font-weight:600;color:#f1f5f9}
.rp-header .sub{font-size:.9rem;color:#94a3b8}
.rp-header .links{margin-top:.5rem;font-size:.85rem}
.rp-header .links a{color:#60a5fa;text-decoration:none;margin-right:1rem}
.rp-section{margin-top:2rem}
.rp-section h3{margin:0 0 .75rem;font-size:.85rem;font-weight:600;text-transform:uppercase;letter-spacing:.08em;color:#64748b}
.exp-item{background:#1e293b;border-radius:.5rem;padding:1rem 1.25rem;margin-bottom:.75rem}
.exp-item .title{font-weight:600;color:#f1f5f9}
.exp-item .meta{font-size:.82rem;color:#94a3b8;margin-top:.15rem}
.exp-item ul{margin:.5rem 0 0;padding-left:1.25em;font-size:.875rem;color:#cbd5e1}
.exp-item ul li{margin:.2rem 0}
.skill-grid{display:flex;flex-wrap:wrap;gap:.4rem}
.skill-chip{background:#1e293b;border-radius:.25rem;padding:.25rem .6rem;font-size:.8rem;color:#cbd5e1}
.skill-chip.expert{border-left:2px solid #34d399}
.skill-chip.advanced{border-left:2px solid #60a5fa}
.skill-chip.intermediate{border-left:2px solid #f59e0b}
.proj-item{background:#1e293b;border-radius:.5rem;padding:.75rem 1rem;margin-bottom:.5rem;display:flex;align-items:baseline;gap:.75rem}
.proj-item .name{font-weight:600;color:#f1f5f9;font-size:.9rem}
.proj-item .tech{font-size:.8rem;color:#94a3b8}
.proj-item a{color:#60a5fa;font-size:.8rem;text-decoration:none;margin-left:auto;white-space:nowrap}
.ach-item{background:#1e293b;border-radius:.5rem;padding:.75rem 1rem;margin-bottom:.5rem;font-size:.875rem;color:#cbd5e1}
.ach-item .metric{color:#34d399;font-weight:600}
.edu-item,.cert-item{background:#1e293b;border-radius:.5rem;padding:.75rem 1rem;margin-bottom:.5rem;font-size:.875rem}
.edu-item .school,.cert-item .name{font-weight:600;color:#f1f5f9}
.edu-item .sub,.cert-item .sub{color:#94a3b8;font-size:.8rem;margin-top:.15rem}
.tag-list{display:flex;flex-wrap:wrap;gap:.35rem}
.tag{background:#1e293b;border-radius:.25rem;padding:.2rem .55rem;font-size:.8rem;color:#94a3b8}
.stat-bar{display:flex;gap:1.5rem;background:#1e293b;border-radius:.5rem;padding:.75rem 1rem;margin-bottom:1.5rem;font-size:.85rem}
.stat-bar span{color:#94a3b8}.stat-bar strong{color:#e2e8f0;margin-left:.25rem}
</style>
<div class="rp">
  <div class="rp-header">
    <h2>{{.Name}}</h2>
    <div class="sub">
      {{if .Email}}{{.Email}}{{end}}
      {{if .Location}} &middot; {{.Location}}{{end}}
    </div>
    {{if .Links}}
    <div class="links">
      {{range $k,$v := .Links}}<a href="{{$v}}" target="_blank" rel="noopener noreferrer">{{$k}}</a>{{end}}
    </div>
    {{end}}
  </div>

  <div style="margin-bottom:1rem"><a href="/admin/resume/edit" style="display:inline-block;padding:.3rem .75rem;background:#1e293b;border:1px solid #334155;border-radius:.375rem;color:#60a5fa;text-decoration:none;font-size:.85rem">&#x270E; Edit resume</a></div>
  <div class="stat-bar">
    <div><span>Experiences</span><strong>{{.Stats.TotalExperiences}}</strong></div>
    <div><span>Skills</span><strong>{{.Stats.TotalSkills}}</strong></div>
    <div><span>Projects</span><strong>{{.Stats.TotalProjects}}</strong></div>
    {{if .Stats.VectorsStored}}<div><span>Vectors</span><strong>{{.Stats.VectorsStored}}</strong></div>{{end}}
    {{if .EnrichedAt}}<div><span>Enriched</span><strong>{{.EnrichedAt}}</strong></div>{{end}}
  </div>

  {{if .Summary}}
  <div class="rp-section">
    <h3>Summary</h3>
    <div style="font-size:.9rem;color:#cbd5e1;line-height:1.6">{{.Summary}}</div>
  </div>
  {{end}}

  {{if .Experiences}}
  <div class="rp-section">
    <h3>Experience</h3>
    {{range .Experiences}}
    <div class="exp-item">
      <div class="title">{{.Title}}{{if .Company}} &ndash; {{.Company}}{{end}}</div>
      <div class="meta">
        {{.StartDate}}{{if .EndDate}} &ndash; {{.EndDate}}{{end}}
        {{if .Location}} &middot; {{.Location}}{{end}}
        {{if .Domain}} &middot; {{.Domain}}{{end}}
      </div>
      {{if .Highlights}}
      <ul>{{range .Highlights}}<li>{{.}}</li>{{end}}</ul>
      {{end}}
    </div>
    {{end}}
  </div>
  {{end}}

  {{if .Skills}}
  <div class="rp-section">
    <h3>Skills</h3>
    <div style="margin-bottom:.75rem;font-size:.8rem;color:#64748b">Border: green=expert · blue=advanced · amber=intermediate</div>
    {{range $cat := skillCategories .Skills}}
    <div style="margin-bottom:.75rem">
      <div style="font-size:.78rem;color:#64748b;margin-bottom:.35rem;text-transform:uppercase;letter-spacing:.05em">{{$cat}}</div>
      <div class="skill-grid">
        {{range $.Skills}}{{if eq .Category $cat}}
        <span class="skill-chip {{.Level}}">{{.Name}}</span>
        {{end}}{{end}}
      </div>
    </div>
    {{end}}
  </div>
  {{end}}

  {{if .Projects}}
  <div class="rp-section">
    <h3>Projects</h3>
    {{range .Projects}}
    <div class="proj-item">
      <span class="name">{{.Name}}</span>
      {{if .Tech}}<span class="tech">{{join .Tech " &middot; "}}</span>{{end}}
      {{if .URL}}<a href="{{.URL}}" target="_blank" rel="noopener noreferrer">&#x2197;</a>{{end}}
    </div>
    {{end}}
  </div>
  {{end}}

  {{if .Achievements}}
  <div class="rp-section">
    <h3>Achievements</h3>
    {{range .Achievements}}
    <div class="ach-item">
      {{.Text}}
      {{if .Metric}}<span class="metric"> ({{.Metric}}{{if .Value}}: {{.Value}}{{end}})</span>{{end}}
      {{if .Context}}<div style="color:#64748b;font-size:.8rem;margin-top:.2rem">{{.Context}}</div>{{end}}
    </div>
    {{end}}
  </div>
  {{end}}

  {{if .Educations}}
  <div class="rp-section">
    <h3>Education</h3>
    {{range .Educations}}
    <div class="edu-item">
      <div class="school">{{.School}}</div>
      <div class="sub">{{.Degree}}{{if .Field}}, {{.Field}}{{end}}
        {{if .StartDate}} &middot; {{.StartDate}}{{if .EndDate}} &ndash; {{.EndDate}}{{end}}{{end}}
      </div>
    </div>
    {{end}}
  </div>
  {{end}}

  {{if .Certifications}}
  <div class="rp-section">
    <h3>Certifications</h3>
    {{range .Certifications}}
    <div class="cert-item">
      <div class="name">{{.Name}}</div>
      <div class="sub">{{if .Issuer}}{{.Issuer}}{{end}}{{if .Year}} &middot; {{.Year}}{{end}}</div>
    </div>
    {{end}}
  </div>
  {{end}}

  {{if .Domains}}
  <div class="rp-section">
    <h3>Domains</h3>
    <div class="tag-list">{{range .Domains}}<span class="tag">{{.}}</span>{{end}}</div>
  </div>
  {{end}}

  {{if .Methodologies}}
  <div class="rp-section">
    <h3>Methodologies</h3>
    <div class="tag-list">{{range .Methodologies}}<span class="tag">{{.}}</span>{{end}}</div>
  </div>
  {{end}}
</div>`
