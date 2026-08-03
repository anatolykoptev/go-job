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
	UWCatalog     []jobs.UpworkCatalogItem // catalog items from upwork_catalog_items
	UWPasteBlocks []jobs.UpworkPasteBlock
	UWMissing     bool
	UWRate        string   // pre-formatted for edit form: "150.00" or ""
	UWAvailability string  // pre-filled availability from upwork_profile
	UWCategories  []string // current categories from upwork_profile (read-only display)
	UWCopyBlocks  []CopyBlockVM // Phase 3: paste blocks rendered via shared copyBlock partial
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
		d.Rate = centsToDollars(profile.HourlyRateCents) + "/hr"
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

// parseDollarsToCents parses a plain-number transport-input string (e.g. "150" or "150.50")
// from an HTML form into integer cents, rounding to the nearest cent.
// Returns (0, nil) for empty string (rate not set).
// Returns an error for invalid or negative values.
//
// This is the internal/adminui transport-input validator. It is distinct from
// engine/jobs parseDollarCents (hunt_map.go) and ParseAmountCents (algora_github.go),
// which handle ingest k/M abbreviation semantics. Do not consolidate across that boundary.
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

// formatCentsToDollars converts cents to a dollar string (e.g. 8500 -> "85").
func formatCentsToDollars(cents int64) string {
	if cents%100 == 0 {
		return strconv.FormatInt(cents/100, 10)
	}
	return strconv.FormatFloat(float64(cents)/100, 'f', 2, 64)
}

// upworkHandler renders the Upwork profile page.
// It accepts auth + csrfKey so it can issue a CSRF token for the edit sub-forms.
func upworkHandler(p *resource.Panel, a auth.Authenticator, csrfKey []byte) http.HandlerFunc {
	tmpl := template.Must(
		template.New("upwork").Funcs(adminuiFuncMap).Parse(sharedPartialsSrc),
	)
	template.Must(tmpl.Parse(upworkTmplSrc))

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
			data.UWCatalog = uwProfile.Catalog
			data.UWPasteBlocks = jobs.FormatUpworkPasteBlocks(uwProfile)
			blocks := data.UWPasteBlocks
			data.UWCopyBlocks = make([]CopyBlockVM, len(blocks))
			for i, b := range blocks {
				data.UWCopyBlocks[i] = CopyBlockVM{
					PreID:    fmt.Sprintf("uw-paste-%d", i),
					FieldNum: i,
					Content:  b.Content,
					Label:    b.Label,
				}
			}
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
func upworkOverviewEditHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// CSRF already verified by MountAction — no verifyCSRF call needed.
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
func upworkSkillCreateHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// CSRF already verified by MountAction — no verifyCSRF call needed.
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
func upworkSkillDeleteHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// CSRF already verified by MountAction — no verifyCSRF call needed.
		id, ok := parseIDParam(w, r)
		if !ok {
			return
		}
		db, personID, ok2 := requireResumeDB(w, r)
		if !ok2 {
			return
		}
		if err := db.DeleteUpworkSkill(r.Context(), personID, id); err != nil {
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
  .uw-label{font-size:.8rem;color:var(--text-secondary,#94a3b8);margin-bottom:.25rem}
  .uw-value{font-size:.9375rem;color:var(--text-primary,#f1f5f9)}
  .uw-overview{white-space:pre-wrap;font-size:.9rem;color:var(--text-secondary,#94a3b8);line-height:1.65}
  .uw-skill-chip{display:inline-block;background:var(--bg-deep,#0f172a);border-radius:9999px;padding:.2rem .7rem;margin:.2rem;font-size:.8125rem;color:var(--text-secondary,#94a3b8);border:1px solid var(--border,#334155)}
  .uw-warning{color:#ef4444;font-size:.8125rem;margin-bottom:.5rem}
  .uw-table{width:100%;border-collapse:collapse;font-size:.875rem}
  .uw-table th{text-align:left;padding:.4rem .6rem;color:var(--text-muted,#64748b);font-weight:600;font-size:.8rem;text-transform:uppercase;letter-spacing:.05em;border-bottom:1px solid var(--border,#334155)}
  .uw-table td{padding:.5rem .6rem;color:var(--text-secondary,#94a3b8);border-bottom:1px solid var(--border-subtle,#1e293b)}
  .uw-empty{color:var(--text-muted,#64748b);font-style:italic;font-size:.875rem}
{{template "sharedCSS" .}}
  .uw-textarea{width:100%;background:var(--bg-deep,#0f172a);border:1px solid var(--border,#334155);border-radius:.375rem;padding:.5rem .75rem;color:var(--text-secondary,#94a3b8);font-family:monospace;font-size:.85rem;resize:vertical;min-height:4rem}
  .uw-form-row{display:flex;gap:.5rem;margin-top:.5rem}
  .uw-input{background:var(--bg-deep,#0f172a);border:1px solid var(--border,#334155);border-radius:.375rem;padding:.35rem .6rem;color:var(--text-primary,#f1f5f9);font-size:.875rem;flex:1}
  .uw-btn{padding:.35rem .75rem;border-radius:.375rem;font-size:.8125rem;cursor:pointer;border:none}
  .uw-btn-primary{background:var(--accent,#3b82f6);color:#0f172a}
  .uw-btn-danger{background:#b91c1c;color:#fff}
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
  <h3>Skills <span style="font-size:.8rem;color:var(--text-secondary,#94a3b8);font-weight:400">(Upwork cap: 15)</span></h3>
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

{{if .UWCopyBlocks}}
<div class="uw-section">
  <h3>Paste Blocks <span style="font-size:.8rem;color:var(--text-secondary,#94a3b8);font-weight:400">(copy into Upwork forms)</span></h3>
  {{range .UWCopyBlocks}}{{template "copyBlock" .}}{{end}}
</div>
{{end}}

<div class="uw-section">
  <h3>Upwork Profile Edit <span style="font-size:.8rem;color:var(--text-secondary,#94a3b8);font-weight:400">(stored in upwork_profile table)</span></h3>
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
    <div class="uw-label">Categories</div>
    <div>{{range .UWCategories}}<span class="uw-skill-chip">{{.}}</span>{{end}}</div>
  </div>
  {{end}}
</div>

<div class="uw-section">
  <h3>Categories Edit <span style="font-size:.8rem;color:var(--text-secondary,#94a3b8);font-weight:400">(full replace)</span></h3>
  <form method="POST" action="/admin/upwork/categories">
    <input type="hidden" name="_csrf" value="{{.CSRFToken}}">
    <div class="uw-label" style="margin-bottom:.5rem">One category per input. Leave blank to remove.</div>
    {{if .UWCategories}}
    {{range .UWCategories}}
    <div class="re-row"><label class="uw-label">Category <input type="text" name="category" class="uw-input" style="margin-bottom:.35rem" value="{{.}}"></label></div>
    {{end}}
    <div class="re-row"><label class="uw-label">Category <input type="text" name="category" class="uw-input" style="margin-bottom:.35rem" placeholder="add category..."></label></div>
    {{else}}
    <div class="re-row"><label class="uw-label">Category <input type="text" name="category" class="uw-input" style="margin-bottom:.35rem" placeholder="e.g. Software Development"></label></div>
    {{end}}
    <button type="submit" class="uw-btn uw-btn-primary" style="margin-top:.5rem">Save Categories</button>
  </form>
</div>

<div class="uw-section">
  <h3>Upwork Skills <span style="font-size:.8rem;color:var(--text-secondary,#94a3b8);font-weight:400">(upwork_skills table — drag to reorder)</span></h3>
  {{if .UWSkills}}
  <ul class="gd-sortable" data-reorder-url="/admin/upwork/skill/reorder" data-csrf="{{.CSRFToken}}" style="list-style:none;padding:0;margin:0 0 .75rem">
    {{range .UWSkills}}
    <li class="gd-sortable-item re-row" data-id="{{.ID}}">
      <div class="name">&#9776; {{.Name}}</div>
      <form method="POST" action="/admin/upwork/skill/{{.ID}}/delete" style="display:inline">
        <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
        <button type="submit" class="uw-btn uw-btn-danger" style="padding:.2rem .5rem;font-size:.75rem">Del</button>
      </form>
    </li>
    {{end}}
  </ul>
  {{else}}<div class="uw-empty" style="margin-bottom:.75rem">no Upwork skills yet</div>{{end}}
  <div class="re-add-form">
    <form method="POST" action="/admin/upwork/skill">
      <input type="hidden" name="_csrf" value="{{.CSRFToken}}">
      <div class="uw-form-row">
        <input type="text" name="name" class="uw-input re-input" placeholder="Skill name (e.g. Go)">
        <button type="submit" class="uw-btn uw-btn-primary">Add Skill</button>
      </div>
    </form>
  </div>
</div>

<div class="uw-section">
  <h3>Catalog Items <span style="font-size:.8rem;color:var(--text-secondary,#94a3b8);font-weight:400">(upwork_catalog_items table — drag to reorder)</span></h3>
  {{if .UWCatalog}}
  <ul class="gd-sortable" data-reorder-url="/admin/upwork/catalog/reorder" data-csrf="{{.CSRFToken}}" style="list-style:none;padding:0;margin:0 0 .75rem">
    {{range .UWCatalog}}
    <li class="gd-sortable-item re-row" data-id="{{.ID}}">
      <div class="name">&#9776; {{.Title}}</div>
      {{if .Description}}<div class="meta">{{.Description}}</div>{{end}}
      <form method="POST" action="/admin/upwork/catalog/{{.ID}}/delete" style="display:inline">
        <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
        <button type="submit" class="uw-btn uw-btn-danger" style="padding:.2rem .5rem;font-size:.75rem">Del</button>
      </form>
    </li>
    {{end}}
  </ul>
  {{else}}<div class="uw-empty" style="margin-bottom:.75rem">no catalog items yet</div>{{end}}
  <div class="re-add-form">
    <h4 style="font-size:.8rem;color:var(--text-muted,#64748b);margin:0 0 .5rem;font-weight:600">Add catalog item</h4>
    <form method="POST" action="/admin/upwork/catalog">
      <input type="hidden" name="_csrf" value="{{.CSRFToken}}">
      <div style="margin-bottom:.35rem">
        <label class="uw-label">Title <abbr title="required">*</abbr></label>
        <input type="text" name="title" class="uw-input re-input" placeholder="e.g. Go Microservices Platform" required aria-required="true">
      </div>
      <div style="margin-bottom:.35rem">
        <label class="uw-label">Description</label>
        <input type="text" name="description" class="uw-input re-input" placeholder="Short description for paste block">
      </div>
      <button type="submit" class="uw-btn uw-btn-primary">Add Item</button>
    </form>
  </div>
</div>`

// upworkCatalogCreateHandler handles POST /admin/upwork/catalog.
// Inserts a new catalog item for the current person (person-scoped).
func upworkCatalogCreateHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// CSRF already verified by MountAction — no verifyCSRF call needed.
		db, personID, ok := requireResumeDB(w, r)
		if !ok {
			return
		}
		title := strings.TrimSpace(r.FormValue("title"))
		if title == "" {
			http.Error(w, "title is required", http.StatusBadRequest)
			return
		}
		description := r.FormValue("description")
		if _, err := db.InsertUpworkCatalogItem(r.Context(), personID, title, description); err != nil {
			slog.Error("upworkCatalogCreateHandler: InsertUpworkCatalogItem", "err", err)
			http.Error(w, "insert failed", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/admin/upwork", http.StatusSeeOther)
	}
}

// upworkCatalogDeleteHandler handles POST /admin/upwork/catalog/{id}/delete.
// Deletes the catalog item identified by {id}, scoped to the current person.
func upworkCatalogDeleteHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// CSRF already verified by MountAction — no verifyCSRF call needed.
		id, ok := parseIDParam(w, r)
		if !ok {
			return
		}
		db, personID, ok2 := requireResumeDB(w, r)
		if !ok2 {
			return
		}
		if err := db.DeleteUpworkCatalogItem(r.Context(), personID, id); err != nil {
			slog.Error("upworkCatalogDeleteHandler: DeleteUpworkCatalogItem", "id", id, "err", err)
			http.Error(w, "delete failed", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/admin/upwork", http.StatusSeeOther)
	}
}

// upworkCatalogReorderHandler handles POST /admin/upwork/catalog/reorder.
// Accepts repeated "id" form fields in desired display order (or comma-sep "order" field).
// Normalizes upwork_catalog_items positions to contiguous 1..N per person.
func upworkCatalogReorderHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// CSRF already verified by MountAction — no verifyCSRF call needed.
		db, personID, ok := requireResumeDB(w, r)
		if !ok {
			return
		}
		ids, err := parseOrderedIDs(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if len(ids) == 0 {
			http.Redirect(w, r, "/admin/upwork", http.StatusSeeOther)
			return
		}
		if err := db.ReorderUpworkCatalogItems(r.Context(), personID, ids); err != nil {
			slog.Error("upworkCatalogReorderHandler: ReorderUpworkCatalogItems", "err", err)
			http.Error(w, "reorder failed", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/admin/upwork", http.StatusSeeOther)
	}
}

// upworkSkillReorderHandler handles POST /admin/upwork/skill/reorder.
// Accepts repeated "id" form fields in desired display order (or comma-sep "order" field).
// Normalizes upwork_skills positions to contiguous 1..N per person.
func upworkSkillReorderHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// CSRF already verified by MountAction — no verifyCSRF call needed.
		db, personID, ok := requireResumeDB(w, r)
		if !ok {
			return
		}
		ids, err := parseOrderedIDs(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if len(ids) == 0 {
			http.Redirect(w, r, "/admin/upwork", http.StatusSeeOther)
			return
		}
		if err := db.ReorderUpworkSkills(r.Context(), personID, ids); err != nil {
			slog.Error("upworkSkillReorderHandler: ReorderUpworkSkills", "err", err)
			http.Error(w, "reorder failed", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/admin/upwork", http.StatusSeeOther)
	}
}

// upworkCategoriesEditHandler handles POST /admin/upwork/categories.
// Read-modify-write: preserves existing title/overview/hourly_rate/availability
// and does a full-replace of categories from repeated "category" form fields.
// This is the single source of truth for categories (#118 invariant).
func upworkCategoriesEditHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// CSRF already verified by MountAction — no verifyCSRF call needed.
		db, personID, ok := requireResumeDB(w, r)
		if !ok {
			return
		}

		// Read existing profile to preserve title/overview/hourly_rate/availability.
		existing, err := db.GetUpworkProfile(r.Context(), personID)
		if err != nil {
			slog.Error("upworkCategoriesEditHandler: GetUpworkProfile", "err", err)
			http.Error(w, "load failed", http.StatusInternalServerError)
			return
		}

		var title, overview, availability string
		var hourlyRate int64
		if !existing.Missing && existing.Profile != nil {
			title = existing.Profile.Title
			overview = existing.Profile.Overview
			hourlyRate = existing.Profile.HourlyRate
			availability = existing.Profile.Availability
		}

		// Full-replace categories from repeated "category" form fields.
		rawCats := r.Form["category"]
		categories := make([]string, 0, len(rawCats))
		for _, c := range rawCats {
			if trimmed := strings.TrimSpace(c); trimmed != "" {
				categories = append(categories, trimmed)
			}
		}

		if err := db.UpsertUpworkProfile(r.Context(), personID, title, overview, hourlyRate, categories, availability); err != nil {
			slog.Error("upworkCategoriesEditHandler: UpsertUpworkProfile", "err", err)
			http.Error(w, "update failed", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/admin/upwork", http.StatusSeeOther)
	}
}

// parseOrderedIDs parses repeated "id" form values or a comma-separated "order" field
// into a slice of positive integers. Returns an error on invalid input.
func parseOrderedIDs(r *http.Request) ([]int, error) {
	rawIDs := r.Form["id"]
	if len(rawIDs) == 0 {
		// Fallback: comma-separated "order" field (e.g., from drag-drop JS).
		order := r.FormValue("order")
		if order != "" {
			rawIDs = strings.Split(order, ",")
		}
	}
	ids := make([]int, 0, len(rawIDs))
	for _, s := range rawIDs {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("invalid id %q: must be a positive integer", s)
		}
		ids = append(ids, n)
	}
	return ids, nil
}
