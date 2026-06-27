package adminui

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/anatolykoptev/go-panel/auth"
	"github.com/anatolykoptev/go-panel/csrf"
	"github.com/anatolykoptev/go-panel/resource"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
)

const navIDUpwork = "upwork"

type upworkPageData struct {
	NavID       string
	Title       string
	TitleLen    int
	Overview    string
	OverviewLen int
	Rate        string
	Skills      []string
	SkillsOver  bool
	SkillCount  int
	Employment  []upworkEmploymentItem
	Portfolio   []upworkPortfolioItem
	// Edit-form data (Upwork-specific tables).
	CSRFToken     string
	UWSkills      []jobs.UpworkSkillRecord
	UWPasteBlocks []jobs.UpworkPasteBlock
	UWMissing     bool
	UWRate        string   // pre-formatted for edit form: "150.00" or ""
	UWAvailability string  // pre-filled availability from upwork_profile
	UWCategories  []string // current categories from upwork_profile (read-only display)
}

type upworkEmploymentItem struct {
	Title     string
	Company   string
	StartDate string
	EndDate   string
}

type upworkPortfolioItem struct {
	Name string
	Tech string
	URL  string
}

func buildUpworkPageData(profile *jobs.ResumeProfileResult) upworkPageData {
	d := upworkPageData{
		NavID:       navIDUpwork,
		Title:       profile.Headline,
		TitleLen:    len([]rune(profile.Headline)),
		Overview:    profile.Summary,
		OverviewLen: len([]rune(profile.Summary)),
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
	for _, p := range profile.Projects {
		d.Portfolio = append(d.Portfolio, upworkPortfolioItem{
			Name: p.Name,
			Tech: strings.Join(p.Tech, ", "),
			URL:  p.URL,
		})
	}
	return d
}

// centsToDollars converts an int64 cent amount to a dollar string (e.g. 15000 -> "$150.00").
// Returns empty string for zero, safe to use with template {{if .Rate}} guards.
// This is the single render site for upwork_profile.hourly_rate display in the edit form.
func centsToDollars(cents int64) string {
	if cents == 0 {
		return ""
	}
	return fmt.Sprintf("$%.2f", float64(cents)/100)
}

// parseDollarsToCents parses a dollar amount string (e.g. "150" or "150.50")
// and returns the value in cents, rounding to the nearest cent.
// Returns (0, nil) for empty string (rate not set).
// Returns an error for invalid or negative values.
func parseDollarsToCents(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	rate, err := strconv.ParseFloat(s, 64)
	if err != nil || rate < 0 {
		return 0, errors.New("invalid hourly_rate: must be a number")
	}
	return int64(math.Round(rate * 100)), nil
}

// upworkHandler renders the Upwork profile page.
// It accepts auth + csrfKey so it can issue a CSRF token for the edit sub-forms.
func upworkHandler(p *resource.Panel, a auth.Authenticator, csrfKey []byte) http.HandlerFunc {
	tmpl := template.Must(template.New("upwork").Funcs(template.FuncMap{
		tmplFuncCharClass: charCounterClass,
		tmplFuncCharLabel: charCounterLabel,
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

		// Load Upwork-specific profile from upwork_profile tables.
		// upwork_profile is the single source of truth for Upwork title, rate,
		// availability, and categories. The read-only preview (Title/Rate sections)
		// also draws from upwork_profile when a row exists, overriding the
		// resume_persons fields shown before the Upwork profile is created.
		uwProfile, uwErr := db.GetUpworkProfile(r.Context(), personID)
		if uwErr != nil {
			slog.Warn("upworkHandler: GetUpworkProfile", "err", uwErr)
		} else {
			data.UWMissing = uwProfile.Missing
			data.UWSkills = uwProfile.Skills
			data.UWPasteBlocks = jobs.FormatUpworkPasteBlocks(uwProfile)
			if !uwProfile.Missing {
				// upwork_profile is authoritative for Upwork-specific display.
				if uwProfile.Profile.HourlyRate > 0 {
					data.UWRate = fmt.Sprintf("%.2f", float64(uwProfile.Profile.HourlyRate)/100)
					// Override the resume_persons-derived Rate display with upwork_profile value.
					data.Rate = centsToDollars(uwProfile.Profile.HourlyRate) + "/hr"
				}
				if uwProfile.Profile.Title != "" {
					// Override title display with upwork_profile.title (Upwork-specific).
					data.Title = uwProfile.Profile.Title
					data.TitleLen = len([]rune(uwProfile.Profile.Title))
				}
				data.UWAvailability = uwProfile.Profile.Availability
				data.UWCategories = uwProfile.Profile.Categories
			}
		}

		// Issue CSRF token for the edit sub-forms.
		sessVal := sessionValue(r, a.(cookieNamer).SessionCookieName())
		data.CSRFToken = csrf.Issue(csrfKey, sessVal, csrf.DefaultTTL)

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

// upworkOverviewEditHandler handles POST /admin/upwork/overview.
// Saves title, overview, hourly_rate, and availability to upwork_profile.
// upwork_profile is the single source of truth for Upwork-specific fields;
// resume_persons.headline/hourly_rate remain the general resume fields.
// Categories are read-modify-write: existing values are preserved unless
// a future categories editor is added.
func upworkOverviewEditHandler(a auth.Authenticator, csrfKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !verifyCSRF(w, r, a, csrfKey) {
			return
		}
		db, personID, ok := requireResumeDB(w, r)
		if !ok {
			return
		}
		title := r.FormValue("title")
		overview := r.FormValue("overview")
		availability := r.FormValue("availability")
		rateCents, rateErr := parseDollarsToCents(r.FormValue("hourly_rate"))
		if rateErr != nil {
			http.Error(w, rateErr.Error(), http.StatusBadRequest)
			return
		}

		// Read-modify-write: preserve existing categories so a re-save of
		// title/overview/rate does not wipe categories set by a future editor.
		var categories []string
		existing, getErr := db.GetUpworkProfile(r.Context(), personID)
		if getErr == nil && !existing.Missing {
			categories = existing.Profile.Categories
		}

		if err := db.UpsertUpworkProfile(r.Context(), personID, title, overview, rateCents, categories, availability); err != nil {
			slog.Error("upworkOverviewEditHandler: UpsertUpworkProfile", "err", err)
			http.Error(w, "update failed", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/admin/upwork", http.StatusSeeOther)
	}
}

// upworkSkillCreateHandler handles POST /admin/upwork/skill.
func upworkSkillCreateHandler(a auth.Authenticator, csrfKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !verifyCSRF(w, r, a, csrfKey) {
			return
		}
		db, personID, ok := requireResumeDB(w, r)
		if !ok {
			return
		}
		name := r.FormValue("name")
		if name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		if _, err := db.InsertUpworkSkill(r.Context(), personID, name); err != nil {
			slog.Error("upworkSkillCreateHandler: InsertUpworkSkill", "err", err)
			http.Error(w, "insert failed", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/admin/upwork", http.StatusSeeOther)
	}
}

// upworkSkillDeleteHandler handles POST /admin/upwork/skill/{id}/delete.
func upworkSkillDeleteHandler(a auth.Authenticator, csrfKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !verifyCSRF(w, r, a, csrfKey) {
			return
		}
		id, ok := parseIDParam(w, r)
		if !ok {
			return
		}
		db, _, ok2 := requireResumeDB(w, r)
		if !ok2 {
			return
		}
		if err := db.DeleteUpworkSkill(r.Context(), id); err != nil {
			slog.Error("upworkSkillDeleteHandler: DeleteUpworkSkill", "id", id, "err", err)
			http.Error(w, "delete failed", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/admin/upwork", http.StatusSeeOther)
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
  .cc-muted{font-size:.6875rem;color:var(--text-muted,#64748b);margin-left:.375rem}
  .cc-green{font-size:.6875rem;color:var(--green,#34d399);margin-left:.375rem}
  .cc-amber{font-size:.6875rem;color:#f59e0b;margin-left:.375rem}
  .cc-red{font-size:.6875rem;color:#ef4444;margin-left:.375rem}
  .uw-textarea{width:100%;background:var(--bg-deep,#0f172a);border:1px solid var(--border,#334155);border-radius:.375rem;padding:.5rem .75rem;color:var(--text-secondary,#94a3b8);font-family:monospace;font-size:.85rem;resize:vertical;min-height:4rem}
  .uw-form-row{display:flex;gap:.5rem;margin-top:.5rem}
  .uw-input{background:var(--bg-deep,#0f172a);border:1px solid var(--border,#334155);border-radius:.375rem;padding:.35rem .6rem;color:var(--text-primary,#f1f5f9);font-size:.875rem;flex:1}
  .uw-btn{padding:.35rem .75rem;border-radius:.375rem;font-size:.8125rem;cursor:pointer;border:none}
  .uw-btn-primary{background:var(--accent,#60a5fa);color:#0f172a}
  .uw-btn-danger{background:#ef4444;color:#fff}
</style>

<div class="page-header">
  <h2>&#x1F7E2; Upwork Profile</h2>
  <p>Upwork-specific data from <code>upwork_profile</code> table. General resume fields editable via <a href="/admin/resume/edit" style="color:var(--accent,#60a5fa)">Resume Edit</a>.</p>
</div>

<div class="uw-section">
  <h3>Title / Headline
    {{if gt .TitleLen 0}}<span class="{{charClass .TitleLen 70}}">{{charLabel .TitleLen 70}}</span>{{end}}
  </h3>
  <div class="uw-label">Upwork profile title (max 70 chars) — from upwork_profile</div>
  <div class="uw-value">{{if .Title}}{{.Title}}{{else}}<span class="uw-empty">not set — fill in the form below</span>{{end}}</div>
</div>

<div class="uw-section">
  <h3>Hourly Rate</h3>
  <div class="uw-label">From upwork_profile — single source of truth for Upwork rate</div>
  <div class="uw-value">{{if .Rate}}{{.Rate}}{{else}}<span class="uw-empty">not set — fill in the form below</span>{{end}}</div>
</div>

<div class="uw-section">
  <h3>Professional Overview
    {{if gt .OverviewLen 0}}<span class="{{charClass .OverviewLen 5000}}">{{charLabel .OverviewLen 5000}}</span>{{end}}
  </h3>
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
</div>

<div class="uw-section">
  <h3>Portfolio</h3>
  {{if .Portfolio}}
  <table class="uw-table">
    <thead><tr><th>Name</th><th>Tech</th><th>URL</th></tr></thead>
    <tbody>
    {{range .Portfolio}}
    <tr><td>{{.Name}}</td><td>{{.Tech}}</td><td>{{if .URL}}<a href="{{.URL}}" style="color:var(--accent,#60a5fa)">{{.URL}}</a>{{end}}</td></tr>
    {{end}}
    </tbody>
  </table>
  {{else}}<div class="uw-empty">no portfolio entries</div>{{end}}
</div>

{{if .UWPasteBlocks}}
<div class="uw-section">
  <h3>Paste Blocks <span style="font-size:.8rem;color:var(--text-muted,#64748b);font-weight:400">(copy into Upwork forms)</span></h3>
  {{range .UWPasteBlocks}}
  <div style="margin-bottom:1rem">
    <div class="uw-label">{{.Label}}</div>
    <textarea class="uw-textarea" readonly rows="4">{{.Content}}</textarea>
  </div>
  {{end}}
</div>
{{end}}

<div class="uw-section">
  <h3>Upwork Profile Edit <span style="font-size:.8rem;color:var(--text-muted,#64748b);font-weight:400">(stored in upwork_profile table)</span></h3>
  {{if .UWMissing}}<div class="uw-empty" style="margin-bottom:.75rem">No Upwork profile entry yet — fill in the form below to create one.</div>{{end}}
  <form method="POST" action="/admin/upwork/overview" style="margin-bottom:1rem">
    <input type="hidden" name="_csrf" value="{{.CSRFToken}}">
    <div style="margin-bottom:.5rem">
      <label class="uw-label">Title (max 70 chars)</label>
      <input type="text" name="title" class="uw-input" value="{{.Title}}" maxlength="70">
    </div>
    <div style="margin-bottom:.5rem">
      <label class="uw-label">Hourly Rate (USD, e.g. 150.00)</label>
      <input type="text" name="hourly_rate" class="uw-input" value="{{.UWRate}}" placeholder="150.00">
    </div>
    <div style="margin-bottom:.5rem">
      <label class="uw-label">Availability</label>
      <input type="text" name="availability" class="uw-input" value="{{.UWAvailability}}" placeholder="30+ hrs/week">
    </div>
    <div style="margin-bottom:.5rem">
      <label class="uw-label">Professional Overview (max 5000 chars)</label>
      <textarea name="overview" class="uw-textarea" rows="8" maxlength="5000">{{.Overview}}</textarea>
    </div>
    <button type="submit" class="uw-btn uw-btn-primary">Save Overview</button>
  </form>
  {{if .UWCategories}}
  <div style="margin-top:.75rem">
    <div class="uw-label">Categories (read-only — seeded via SQL)</div>
    <div>{{range .UWCategories}}<span class="uw-skill-chip">{{.}}</span>{{end}}</div>
  </div>
  {{end}}
</div>

<div class="uw-section">
  <h3>Upwork Skills <span style="font-size:.8rem;color:var(--text-muted,#64748b);font-weight:400">(upwork_skills table)</span></h3>
  {{if .UWSkills}}
  <div style="margin-bottom:.75rem">
    {{range .UWSkills}}
    <span class="uw-skill-chip">
      {{.Name}}
      <form method="POST" action="/admin/upwork/skill/{{.ID}}/delete" style="display:inline">
        <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
        <button type="submit" style="background:none;border:none;color:#ef4444;cursor:pointer;font-size:.75rem;padding:0 0 0 .25rem">✕</button>
      </form>
    </span>
    {{end}}
  </div>
  {{else}}<div class="uw-empty" style="margin-bottom:.75rem">no Upwork skills yet</div>{{end}}
  <form method="POST" action="/admin/upwork/skill">
    <input type="hidden" name="_csrf" value="{{.CSRFToken}}">
    <div class="uw-form-row">
      <input type="text" name="name" class="uw-input" placeholder="Skill name (e.g. Go)">
      <button type="submit" class="uw-btn uw-btn-primary">Add Skill</button>
    </div>
  </form>
</div>`
