package adminui

import (
	"bytes"
	"encoding/json"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/anatolykoptev/go-panel/auth"
	"github.com/anatolykoptev/go-panel/csrf"
	"github.com/anatolykoptev/go-panel/render"
	"github.com/anatolykoptev/go-panel/resource"
	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/jackc/pgx/v5"
)

// jobDetailQuery selects columns from hunt_jobs for a single row, including
// score_rationale JSONB for fit-card rendering.
const jobDetailQuery = `
SELECT id, COALESCE(title,''), COALESCE(company,''), COALESCE(url,''),
       COALESCE(source,''), COALESCE(location,''), COALESCE(remote,''),
       COALESCE(job_type,''), COALESCE(experience,''),
       salary_min, salary_max, COALESCE(salary_currency,''), COALESCE(salary_interval,''),
       COALESCE(status,''),
       fit_score, COALESCE(fit_band,''), COALESCE(success_band,''), COALESCE(over_under,''),
       scored_at, posted_at, first_seen_at, last_seen_at,
       COALESCE(description,''),
       recommendation_rank, COALESCE(recommendation_tier,''), COALESCE(recommendation_note,''),
       score_rationale
  FROM hunt_jobs
 WHERE id = $1`

// currentRating holds the current hunt_ratings row for a job.
type currentRating struct {
	Stage     string
	Note      string
	UpdatedAt time.Time
}

// jobDetailData is the template context for the job detail page.
type jobDetailData struct {
	ID              int64
	Title           string
	Company         string
	URL             string
	Source          string
	Location        string
	Remote          string
	JobType         string
	Experience      string
	SalaryMin       *int
	SalaryMax       *int
	SalaryCurrency  string
	SalaryInterval  string
	Status          string
	FitScore        *int
	FitBand         string
	SuccessBand     string
	OverUnder       string
	ScoredAt        *time.Time
	PostedAt        *time.Time
	FirstSeenAt     *time.Time
	LastSeenAt      *time.Time
	DescriptionHTML template.HTML
	RecRank         *int
	RecTier         string
	RecNote         string
	SalaryDisplay   string
	// FitCardHTML holds the pre-rendered FIT ASSESSMENT card (two-column WHY YOU / GAPS).
	// Built from score_rationale JSONB; empty string when job is unscored.
	// XSS-safe: buildFitCardHTML uses html.EscapeString on all LLM free-text.
	FitCardHTML template.HTML
	// MarketCardHTML holds the pre-rendered MARKET READ card (chip + over/under + disclaimer).
	// Built from success_band / over_under / score_rationale.success_reasoning.
	// XSS-safe: buildMarketCardHTML uses html.EscapeString on free-text.
	MarketCardHTML template.HTML
	// CSRF token for write forms on this page.
	CSRFToken string
	// Rating holds the current hunt_ratings row; nil when not yet rated.
	Rating *currentRating
}

// jobDetailTmplFuncs are the template helper functions used in the detail page.
var jobDetailTmplFuncs = template.FuncMap{
	"dateStr":  dateStr,
	"derefInt": intStr,
}

// jobDetailTmplSrc is the HTML content fragment for the job detail page.
// This is embedded inside the go-panel shell.Layout via renderShell.
// Kept as a const so tests can inspect it without executing.
const jobDetailTmplSrc = `<style>
.page{max-width:860px;margin:0 auto;padding:2rem 1.5rem}
.back{display:inline-block;margin-bottom:1.5rem;color:#60a5fa;text-decoration:none;font-size:.9rem}
.back:hover{color:#93c5fd}
h2{margin:0 0 1.5rem;font-size:1.5rem;font-weight:600;color:#f1f5f9}
.meta-grid{display:grid;grid-template-columns:1fr 1fr;gap:.5rem 2rem;margin-bottom:1.5rem;font-size:.9rem}
.meta-grid dt{color:#94a3b8;font-weight:500}
.meta-grid dd{margin:0;color:#e2e8f0;word-break:break-word}
.section{margin-top:1.5rem}
.section h3{margin:0 0 .75rem;font-size:1rem;font-weight:600;color:#94a3b8;text-transform:uppercase;letter-spacing:.05em}
.md-body{background:#1e293b;border-radius:.5rem;padding:1.25rem;font-size:.9rem;line-height:1.65;color:#cbd5e1}
.md-body h2{font-size:1.1rem;margin:.75rem 0 .4rem;color:#e2e8f0}
.md-body h3{font-size:1rem;margin:.6rem 0 .3rem;color:#e2e8f0}
.md-body p{margin:.4rem 0}
.md-body ul,.md-body ol{padding-left:1.4em;margin:.4rem 0}
.md-body code{background:#334155;padding:.1em .35em;border-radius:.25rem;font-size:.85em}
.md-body pre{background:#334155;padding:.75rem 1rem;border-radius:.35rem;overflow-x:auto}
.apply-btn{display:inline-block;margin-top:1.5rem;padding:.6rem 1.25rem;background:#2563eb;color:#fff;border-radius:.35rem;text-decoration:none;font-weight:500;font-size:.9rem}
.apply-btn:hover{background:#1d4ed8}
.dl-link{display:inline-block;margin-top:.75rem;margin-right:.75rem;padding:.45rem 1rem;background:#334155;color:#e2e8f0;border-radius:.35rem;text-decoration:none;font-size:.85rem}
.dl-link:hover{background:#475569}
.rate-form{margin-top:1.5rem;background:#1e293b;border-radius:.5rem;padding:1.25rem}
.rate-form h3{margin:0 0 .75rem;font-size:1rem;font-weight:600;color:#94a3b8;text-transform:uppercase;letter-spacing:.05em}
.current-rating{margin-bottom:1rem;padding:.75rem 1rem;background:#0f172a;border-radius:.35rem;font-size:.85rem}
.current-rating .stage{font-weight:600;color:#34d399}
.current-rating .note{color:#94a3b8;margin-top:.25rem}
.rate-form label{display:block;margin-bottom:.75rem;font-size:.875rem}
.rate-form label span{color:#94a3b8;font-size:.75rem;display:block;margin-bottom:.25rem}
.rate-form select,.rate-form textarea{width:100%;padding:.5rem .75rem;background:#0f172a;border:1px solid #334155;border-radius:.375rem;color:#e2e8f0;font-size:.875rem}
.rate-form textarea{min-height:5rem;resize:vertical}
.rate-form button{margin-top:.5rem;padding:.5rem 1.25rem;background:#2563eb;color:#fff;border:none;border-radius:.375rem;cursor:pointer;font-size:.875rem}
.rate-form button:hover{background:#1d4ed8}
/* Fit/Market assessment cards */
.fit-card-cols{display:grid;grid-template-columns:1fr 1fr;gap:1rem;margin-top:.5rem}
.fit-col-label{font-size:.75rem;font-weight:600;color:#94a3b8;text-transform:uppercase;letter-spacing:.05em;margin-bottom:.5rem}
.fit-col-list{margin:0;padding-left:1.2em;font-size:.875rem;color:#cbd5e1}
.fit-col-list li{margin-bottom:.25rem}
.market-card{background:#1e293b;border-radius:.5rem;padding:1rem}
.market-card-header{display:flex;align-items:center;gap:.75rem;margin-bottom:.75rem}
.market-card-label{font-size:.75rem;font-weight:600;color:#94a3b8;text-transform:uppercase;letter-spacing:.05em}
.market-reasoning{font-size:.875rem;color:#cbd5e1;margin-bottom:.75rem}
.market-disclaimer{font-size:.75rem;color:#64748b;border-top:1px solid #334155;padding-top:.5rem;margin-top:.5rem}
/* Chip styles */
.fit-chip{display:inline-flex;align-items:center;gap:.25rem;padding:.15rem .5rem;border-radius:.75rem;font-size:.8rem;font-weight:600}
.fit-strong{background:#14532d;color:#86efac}.fit-moderate{background:#1e3a5f;color:#93c5fd}
.fit-weak{background:#78350f;color:#fcd34d}.fit-low{background:#4c1d21;color:#fca5a5}
.fit-reject{background:#3b1c1c;color:#f87171}.fit-unscored{background:#1e293b;color:#64748b}
.suc-chip{display:inline-flex;align-items:center;gap:.25rem;padding:.15rem .5rem;border-radius:.75rem;font-size:.8rem;font-weight:600}
.suc-strong{background:#14532d;color:#86efac}.suc-moderate{background:#1e3a5f;color:#93c5fd}
.suc-longshot{background:#4c1d21;color:#fca5a5}.suc-none{color:#64748b}
.ou-glyph{font-size:.85rem}.ou-over{color:#fbbf24}.ou-match{color:#34d399}.ou-under{color:#f87171}
</style>
<div class="page">
  <a class="back" href="/admin/jobs">&larr; Jobs</a>
  <h2>{{.Title}}</h2>
  <dl class="meta-grid">
    {{if .Company}}<dt>Company</dt><dd>{{.Company}}</dd>{{end}}
    {{if .Status}}<dt>Status</dt><dd>{{.Status}}</dd>{{end}}
    {{if .Location}}<dt>Location</dt><dd>{{.Location}}</dd>{{end}}
    {{if .Remote}}<dt>Remote</dt><dd>{{.Remote}}</dd>{{end}}
    {{if .SalaryDisplay}}<dt>Salary</dt><dd>{{.SalaryDisplay}}</dd>{{end}}
    {{if .Source}}<dt>Source</dt><dd>{{.Source}}</dd>{{end}}
    {{if .PostedAt}}<dt>Posted</dt><dd>{{dateStr .PostedAt}}</dd>{{end}}
    {{if .LastSeenAt}}<dt>Last Seen</dt><dd>{{dateStr .LastSeenAt}}</dd>{{end}}
    {{if .FitScore}}<dt>Fit Score</dt><dd>{{derefInt .FitScore}}{{if .FitBand}} ({{.FitBand}}){{end}}</dd>{{end}}
    {{if .RecRank}}<dt>Rec Rank</dt><dd>#{{derefInt .RecRank}}{{if .RecTier}} tier {{.RecTier}}{{end}}</dd>{{end}}
    {{if .RecNote}}<dt>Rec Note</dt><dd>{{.RecNote}}</dd>{{end}}
    {{if .JobType}}<dt>Type</dt><dd>{{.JobType}}</dd>{{end}}
    {{if .Experience}}<dt>Experience</dt><dd>{{.Experience}}</dd>{{end}}
  </dl>
  {{if .URL}}<a class="apply-btn" href="{{.URL}}" target="_blank" rel="noopener noreferrer">Apply &#x2197;</a>{{end}}
  <a class="dl-link" href="/admin/jobs/{{.ID}}/download/resume">Resume PDF</a>
  <a class="dl-link" href="/admin/jobs/{{.ID}}/download/cover">Cover PDF</a>
  {{if .FitCardHTML}}
  <div class="section">
    <h3>Fit Assessment</h3>
    {{.FitCardHTML}}
  </div>
  {{end}}
  {{if .MarketCardHTML}}
  <div class="section">
    {{.MarketCardHTML}}
  </div>
  {{end}}
  {{if .DescriptionHTML}}
  <div class="section">
    <h3>Description</h3>
    <div class="md-body">{{.DescriptionHTML}}</div>
  </div>
  {{end}}
  <div class="rate-form">
    <h3>Rate</h3>
    {{if .Rating}}
    <div class="current-rating">
      <span class="stage">Current stage: {{.Rating.Stage}}</span>
      {{if .Rating.Note}}<div class="note">{{.Rating.Note}}</div>{{end}}
    </div>
    {{end}}
    <form method="POST" action="/admin/jobs/{{.ID}}/rate">
      <input type="hidden" name="_csrf" value="{{.CSRFToken}}">
      <label>
        <span>Stage</span>
        <select name="stage">
          <option value="new"{{if and .Rating (eq .Rating.Stage "new")}} selected{{end}}>new</option>
          <option value="interesting"{{if and .Rating (eq .Rating.Stage "interesting")}} selected{{end}}>interesting</option>
          <option value="saved"{{if and .Rating (eq .Rating.Stage "saved")}} selected{{end}}>saved</option>
          <option value="discarded"{{if and .Rating (eq .Rating.Stage "discarded")}} selected{{end}}>discarded</option>
          <option value="claimed"{{if and .Rating (eq .Rating.Stage "claimed")}} selected{{end}}>claimed</option>
        </select>
      </label>
      <label>
        <span>Note</span>
        <textarea name="note">{{if .Rating}}{{.Rating.Note}}{{end}}</textarea>
      </label>
      <button type="submit">Save rating</button>
    </form>
  </div>
</div>`

// jobDetailHandler returns an http.HandlerFunc that renders a single hunt_jobs
// row wrapped in the go-panel shell.Layout chrome. Wrap with a.Require() before
// mounting on the mux.
func jobDetailHandler(p *resource.Panel, store *hunt.Store, adminUser string, a *auth.HMACAuth, csrfKey []byte) http.HandlerFunc {
	tmpl := template.Must(template.New("job_detail").Funcs(jobDetailTmplFuncs).Parse(jobDetailTmplSrc))

	return func(w http.ResponseWriter, r *http.Request) {
		rawID := r.PathValue("id")
		id64, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}

		var d jobDetailData
		var descRaw string
		var rationaleRaw []byte
		row := store.Pool().QueryRow(r.Context(), jobDetailQuery, id64)
		if err := row.Scan(
			&d.ID, &d.Title, &d.Company, &d.URL,
			&d.Source, &d.Location, &d.Remote,
			&d.JobType, &d.Experience,
			&d.SalaryMin, &d.SalaryMax, &d.SalaryCurrency, &d.SalaryInterval,
			&d.Status,
			&d.FitScore, &d.FitBand, &d.SuccessBand, &d.OverUnder,
			&d.ScoredAt, &d.PostedAt, &d.FirstSeenAt, &d.LastSeenAt,
			&descRaw,
			&d.RecRank, &d.RecTier, &d.RecNote,
			&rationaleRaw,
		); err != nil {
			if isJobNotFound(err) {
				http.Error(w, "job not found", http.StatusNotFound)
				return
			}
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}

		d.SalaryDisplay = salaryDetailStr(d.SalaryMin, d.SalaryMax, d.SalaryCurrency, d.SalaryInterval)
		if descRaw != "" {
			d.DescriptionHTML = render.Markdown(descRaw)
		}

		// Parse score_rationale JSONB and build fit/market card HTML.
		// buildFitCardHTML / buildMarketCardHTML are in jobs.go (same package).
		// All LLM free-text is escaped inside those builders — safe to cast to template.HTML.
		var scoreRat struct {
			FitReasons       []string `json:"fit_reasons"`
			FitGaps          []string `json:"fit_gaps"`
			SuccessReasoning string   `json:"success_reasoning"`
		}
		if len(rationaleRaw) > 0 {
			if jsonErr := json.Unmarshal(rationaleRaw, &scoreRat); jsonErr != nil {
				slog.Warn("jobDetailHandler: malformed score_rationale", "id", id64, "err", jsonErr)
			}
		}
		// Fit card: only render when scored (not unscored/stale/nil/reject).
		if d.FitScore != nil && d.FitBand != fitBandUnscored && d.FitBand != fitBandStale && d.FitBand != fitBandReject {
			d.FitCardHTML = template.HTML(buildFitCardHTML(scoreRat.FitReasons, scoreRat.FitGaps)) //nolint:gosec // XSS-safe: builders use html.EscapeString on all free-text
		}
		// Market card: always render (shows "not assessed" state when band is empty).
		d.MarketCardHTML = template.HTML(buildMarketCardHTML(d.SuccessBand, d.OverUnder, scoreRat.SuccessReasoning)) //nolint:gosec // XSS-safe: builders use html.EscapeString on all free-text

		// Fetch current rating via canonical Store method. Not found is OK — just no rating yet.
		rat, ratingErr := store.GetRating(r.Context(), "job", id64, adminUser)
		if ratingErr == nil {
			d.Rating = &currentRating{
				Stage:     rat.Stage,
				Note:      rat.Note,
				UpdatedAt: rat.UpdatedAt,
			}
		} else if !errors.Is(ratingErr, hunt.ErrNotFound) {
			slog.Warn("jobDetailHandler: fetch hunt_ratings", "id", id64, "err", ratingErr)
		}

		// Mint a CSRF token bound to the current session cookie.
		sessVal := sessionValue(r, a.SessionCookieName())
		d.CSRFToken = csrf.Issue(csrfKey, sessVal, csrf.DefaultTTL)

		var buf bytes.Buffer
		if execErr := tmpl.Execute(&buf, d); execErr != nil {
			slog.Error("jobDetailHandler: template execute", "err", execErr)
			http.Error(w, "render error", http.StatusInternalServerError)
			return
		}
		if err := p.RenderPageHTML(w, r, "Job — "+d.Title, "jobs", buf.String()); err != nil {
			slog.Error("adminui: render job_detail", "err", err)
		}
	}
}

// isJobNotFound returns true when the row SELECT matched no job.
func isJobNotFound(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}

func salaryDetailStr(lo, hi *int, cur, interval string) string {
	base := salaryStr(lo, hi)
	if base == "" {
		return ""
	}
	if cur != "" {
		base += " " + cur
	}
	if interval != "" {
		base += "/" + interval
	}
	return base
}
