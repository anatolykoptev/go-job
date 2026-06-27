package adminui

import (
	"bytes"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"

	"github.com/anatolykoptev/go-panel/resource"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
)

const navIDUpwork = "upwork"

type upworkPageData struct {
	NavID      string
	Title      string
	Overview   string
	Rate       string
	Skills     []string
	SkillsOver bool
	SkillCount int
	Employment []upworkEmploymentItem
}

type upworkEmploymentItem struct {
	Title     string
	Company   string
	StartDate string
	EndDate   string
}

func buildUpworkPageData(profile *jobs.ResumeProfileResult) upworkPageData {
	d := upworkPageData{
		NavID:    navIDUpwork,
		Title:    profile.Headline,
		Overview: profile.Summary,
	}
	if profile.HourlyRateCents > 0 {
		d.Rate = fmt.Sprintf("$%.2f/hr", float64(profile.HourlyRateCents)/100)
	}
	const maxSkills = 15
	d.SkillCount = len(profile.Skills)
	d.SkillsOver = d.SkillCount > maxSkills
	skills := profile.Skills
	if len(skills) > maxSkills {
		skills = skills[:maxSkills]
	}
	for _, s := range skills {
		d.Skills = append(d.Skills, s.Name)
	}
	for _, e := range profile.Experiences {
		d.Employment = append(d.Employment, upworkEmploymentItem{
			Title:     e.Title,
			Company:   e.Company,
			StartDate: e.StartDate,
			EndDate:   e.EndDate,
		})
	}
	return d
}

func upworkHandler(p *resource.Panel) http.HandlerFunc {
	tmpl := template.Must(template.New("upwork").Funcs(template.FuncMap{
		"charClass": charCounterClass,
		"charLabel": charCounterLabel,
	}).Parse(upworkTmplSrc))

	return func(w http.ResponseWriter, r *http.Request) {
		db := jobs.GetResumeDB()
		if db == nil {
			if err := p.RenderPageHTML(w, r, "Upwork", navIDUpwork, resumeEmptyHTML("Resume database not configured (set DATABASE_URL).")); err != nil {
				slog.Error("adminui: render upwork", "err", err)
			}
			return
		}
		personID := db.GetLatestPersonID(r.Context())
		if personID == 0 {
			if err := p.RenderPageHTML(w, r, "Upwork", navIDUpwork, resumeEmptyHTML("No resume data yet — run master_resume_build first.")); err != nil {
				slog.Error("adminui: render upwork", "err", err)
			}
			return
		}
		profile, err := jobs.GetResumeProfile(r.Context(), "")
		if err != nil {
			slog.Warn("upworkHandler: GetResumeProfile", "err", err)
			if err2 := p.RenderPageHTML(w, r, "Upwork", navIDUpwork, resumeEmptyHTML("Could not load profile: "+err.Error())); err2 != nil {
				slog.Error("adminui: render upwork", "err", err2)
			}
			return
		}
		data := buildUpworkPageData(profile)
		var buf bytes.Buffer
		if execErr := tmpl.Execute(&buf, data); execErr != nil {
			slog.Error("upworkHandler: template execute", "err", execErr)
			http.Error(w, "render error", http.StatusInternalServerError)
			return
		}
		if err := p.RenderPageHTML(w, r, "Upwork", navIDUpwork, buf.String()); err != nil {
			slog.Error("adminui: render upwork", "err", err)
		}
	}
}

//nolint:gosec // upworkTmplSrc is an HTML/CSS template, not a credential
const upworkTmplSrc = `<style>
  .uw-section{background:var(--bg-surface,#1e293b);border:1px solid var(--border,#334155);border-radius:var(--radius-lg,.75rem);padding:1.25rem 1.5rem;margin-bottom:1.25rem}
  .uw-section h3{font-size:.9375rem;font-weight:700;color:var(--text-primary,#f1f5f9);margin-bottom:.5rem}
  .uw-label{font-size:.8rem;color:var(--text-muted,#64748b);margin-bottom:.25rem}
  .uw-value{font-size:.9375rem;color:var(--text-primary,#f1f5f9)}
  .uw-overview{white-space:pre-wrap;font-size:.9rem;color:var(--text-secondary,#94a3b8);line-height:1.65}
  .uw-skill-chip{display:inline-block;background:var(--bg-deep,#0f172a);border-radius:9999px;padding:.2rem .7rem;margin:.2rem;font-size:.8125rem;color:var(--text-secondary,#94a3b8);border:1px solid var(--border,#334155)}
  .uw-warning{color:#ef4444;font-size:.8125rem;margin-bottom:.5rem}
  .uw-table{width:100%;border-collapse:collapse;font-size:.875rem}
  .uw-table th{text-align:left;padding:.4rem .6rem;color:var(--text-muted,#64748b);font-weight:600;font-size:.8rem;text-transform:uppercase;letter-spacing:.05em;border-bottom:1px solid var(--border,#334155)}
  .uw-table td{padding:.5rem .6rem;color:var(--text-secondary,#94a3b8);border-bottom:1px solid var(--border-subtle,#1e293b)}
  .uw-empty{color:var(--text-muted,#64748b);font-style:italic;font-size:.875rem}
</style>

<div class="page-header">
  <h2>&#x1F7E2; Upwork Profile</h2>
  <p>Read-only preview of how your profile fields will appear on Upwork. Edit via <a href="/admin/resume/edit" style="color:var(--accent,#60a5fa)">Resume Edit</a>.</p>
</div>

<div class="uw-section">
  <h3>Title / Headline</h3>
  <div class="uw-label">Upwork profile title (max 70 chars)</div>
  <div class="uw-value">{{if .Title}}{{.Title}}{{else}}<span class="uw-empty">not set — add via Resume Edit &gt; Upwork Headline</span>{{end}}</div>
</div>

<div class="uw-section">
  <h3>Hourly Rate</h3>
  <div class="uw-value">{{if .Rate}}{{.Rate}}{{else}}<span class="uw-empty">not set — add via Resume Edit &gt; Upwork Hourly Rate</span>{{end}}</div>
</div>

<div class="uw-section">
  <h3>Professional Overview</h3>
  <div class="uw-overview">{{if .Overview}}{{.Overview}}{{else}}<span class="uw-empty">no summary set</span>{{end}}</div>
</div>

<div class="uw-section">
  <h3>Skills <span style="font-size:.8rem;color:var(--text-muted,#64748b);font-weight:400">(Upwork cap: 15)</span></h3>
  {{if .SkillsOver}}<div class="uw-warning">&#x26A0; You have {{.SkillCount}} skills — only the first 15 are shown (Upwork limit).</div>{{end}}
  {{if .Skills}}
    {{range .Skills}}<span class="uw-skill-chip">{{.}}</span>{{end}}
  {{else}}<div class="uw-empty">no skills</div>{{end}}
</div>

<div class="uw-section">
  <h3>Employment History</h3>
  {{if .Employment}}
  <table class="uw-table">
    <thead><tr><th>Title</th><th>Company</th><th>Start</th><th>End</th></tr></thead>
    <tbody>
    {{range .Employment}}
    <tr><td>{{.Title}}</td><td>{{.Company}}</td><td>{{.StartDate}}</td><td>{{if .EndDate}}{{.EndDate}}{{else}}Present{{end}}</td></tr>
    {{end}}
    </tbody>
  </table>
  {{else}}<div class="uw-empty">no employment entries</div>{{end}}
</div>`
