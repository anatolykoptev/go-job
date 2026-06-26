package adminui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
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
	"github.com/jackc/pgx/v5/pgxpool"
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

// jobDetailRecord holds fields scanned from a hunt_jobs row.
type jobDetailRecord struct {
	ID             int64
	Title          string
	Company        string
	URL            string
	Source         string
	Location       string
	Remote         string
	JobType        string
	Experience     string
	SalaryMin      *int
	SalaryMax      *int
	SalaryCurrency string
	SalaryInterval string
	Status         string
	FitScore       *int
	FitBand        string
	SuccessBand    string
	OverUnder      string
	ScoredAt       *time.Time
	PostedAt       *time.Time
	FirstSeenAt    *time.Time
	LastSeenAt     *time.Time
	DescRaw        string
	RecRank        *int
	RecTier        string
	RecNote        string
	RationaleRaw   []byte
}

// applicationSectionTmpl is the html/template for the Rate + download section.
// All DB/user fields are escaped by the template engine. The CSRF token is a
// hex/base64 value from csrf.Issue — safe to inject as a hidden form field.
var applicationSectionTmpl = template.Must(template.New("app_section").Parse(`<div class="rate-form">
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
  <div>
    <a class="dl-link" href="/admin/jobs/{{.ID}}/download/resume">Resume PDF</a>
    <a class="dl-link" href="/admin/jobs/{{.ID}}/download/cover">Cover PDF</a>
  </div>
</div>`))

// applicationSectionData is the template context for the application section.
type applicationSectionData struct {
	ID        int64
	CSRFToken string
	Rating    *currentRating
}

// currentRating holds the current hunt_ratings row for a job.
type currentRating struct {
	Stage     string
	Note      string
	UpdatedAt time.Time
}

// jobDetailer returns a Detailer closure for the jobs resource.
// The closure conforms to resource.Resource.Detailer:
//
//	func(ctx context.Context, r *http.Request, id string) ([]resource.DetailSection, error)
//
// Sections returned:
//  1. Styles  — RawHTML CSS block required by fit/market/description cards
//  2. Overview — key-value pairs (company, location, salary, status, etc.)
//  3. Fit Assessment — RawHTML two-column card (when scored)
//  4. Market Read — RawHTML market card
//  5. Description — RawHTML rendered markdown
//  6. Application — RawHTML rate form + download links
func jobDetailer(pool *pgxpool.Pool, store *hunt.Store, adminUser string, a *auth.HMACAuth, csrfKey []byte) func(ctx context.Context, r *http.Request, id string) ([]resource.DetailSection, error) {
	return func(ctx context.Context, r *http.Request, id string) ([]resource.DetailSection, error) {
		id64, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			return nil, resource.ErrDetailNotFound
		}

		rec, err := scanJobDetail(ctx, pool, id64)
		if err != nil {
			if isJobNotFound(err) {
				return nil, resource.ErrDetailNotFound
			}
			return nil, fmt.Errorf("jobDetailer: scan job %d: %w", id64, err)
		}

		// Parse score_rationale JSONB.
		var scoreRat struct {
			FitReasons       []string `json:"fit_reasons"`
			FitGaps          []string `json:"fit_gaps"`
			SuccessReasoning string   `json:"success_reasoning"`
		}
		if len(rec.RationaleRaw) > 0 {
			if jsonErr := json.Unmarshal(rec.RationaleRaw, &scoreRat); jsonErr != nil {
				slog.WarnContext(ctx, "jobDetailer: malformed score_rationale", "id", id64, "err", jsonErr)
			}
		}

		salaryDisplay := salaryDetailStr(rec.SalaryMin, rec.SalaryMax, rec.SalaryCurrency, rec.SalaryInterval)

		sections := []resource.DetailSection{
			// Styles section: CSS required for fit/market/description cards.
			{RawHTML: jobDetailStyles},
			// Overview section: key/value pairs.
			buildOverviewSection(rec, salaryDisplay),
		}

		// Fit Assessment: only when scored.
		if rec.FitScore != nil && rec.FitBand != fitBandUnscored && rec.FitBand != fitBandStale && rec.FitBand != fitBandReject {
			sections = append(sections, resource.DetailSection{
				Title:   "Fit Assessment",
				RawHTML: buildFitCardHTML(scoreRat.FitReasons, scoreRat.FitGaps),
			})
		}

		// Market Read: always rendered.
		sections = append(sections, resource.DetailSection{
			RawHTML: buildMarketCardHTML(rec.SuccessBand, rec.OverUnder, scoreRat.SuccessReasoning),
		})

		// Description: rendered markdown.
		if rec.DescRaw != "" {
			descHTML := string(render.Markdown(rec.DescRaw))
			sections = append(sections, resource.DetailSection{
				Title:   "Description",
				RawHTML: `<div class="md-body">` + descHTML + `</div>`,
			})
		}

		// Application: rate form + download links.
		// CSRF token is minted bound to the session cookie from the request.
		sessVal := sessionValue(r, a.SessionCookieName())
		csrfTok := csrf.Issue(csrfKey, sessVal, csrf.DefaultTTL)

		rat, ratingErr := store.GetRating(ctx, "job", id64, adminUser)
		var rating *currentRating
		if ratingErr == nil {
			rating = &currentRating{
				Stage:     rat.Stage,
				Note:      rat.Note,
				UpdatedAt: rat.UpdatedAt,
			}
		} else if !errors.Is(ratingErr, hunt.ErrNotFound) {
			slog.WarnContext(ctx, "jobDetailer: fetch hunt_ratings", "id", id64, "err", ratingErr)
		}

		appHTML, err := buildApplicationSectionHTML(id64, csrfTok, rating)
		if err != nil {
			return nil, fmt.Errorf("jobDetailer: build application section: %w", err)
		}
		sections = append(sections, resource.DetailSection{
			Title:   "Application",
			RawHTML: appHTML,
		})

		return sections, nil
	}
}

// scanJobDetail executes jobDetailQuery and scans the result into a jobDetailRecord.
func scanJobDetail(ctx context.Context, pool *pgxpool.Pool, id64 int64) (jobDetailRecord, error) {
	var rec jobDetailRecord
	row := pool.QueryRow(ctx, jobDetailQuery, id64)
	err := row.Scan(
		&rec.ID, &rec.Title, &rec.Company, &rec.URL,
		&rec.Source, &rec.Location, &rec.Remote,
		&rec.JobType, &rec.Experience,
		&rec.SalaryMin, &rec.SalaryMax, &rec.SalaryCurrency, &rec.SalaryInterval,
		&rec.Status,
		&rec.FitScore, &rec.FitBand, &rec.SuccessBand, &rec.OverUnder,
		&rec.ScoredAt, &rec.PostedAt, &rec.FirstSeenAt, &rec.LastSeenAt,
		&rec.DescRaw,
		&rec.RecRank, &rec.RecTier, &rec.RecNote,
		&rec.RationaleRaw,
	)
	return rec, err
}

// buildOverviewSection constructs the Overview DetailSection from a scanned record.
// Plain-text items are escaped by go-panel on render. The Apply link uses HTML:true
// with html.EscapeString applied to the URL — safe to render as markup.
func buildOverviewSection(rec jobDetailRecord, salaryDisplay string) resource.DetailSection {
	var items []resource.DetailItem

	addItem := func(label, value string) {
		if value != "" {
			items = append(items, resource.DetailItem{Label: label, Value: value})
		}
	}
	addTimeItem := func(label string, t *time.Time) {
		if t != nil {
			items = append(items, resource.DetailItem{Label: label, Value: dateStr(t)})
		}
	}

	addItem("Company", rec.Company)
	addItem("Location", rec.Location)
	addItem("Remote", rec.Remote)
	addItem("Salary", salaryDisplay)
	addItem("Status", rec.Status)
	addItem("Type", rec.JobType)
	addItem("Experience", rec.Experience)
	addItem("Source", rec.Source)
	addTimeItem("Posted", rec.PostedAt)
	addTimeItem("Last Seen", rec.LastSeenAt)

	// Fit score display.
	if rec.FitScore != nil {
		fitVal := intStr(rec.FitScore)
		if rec.FitBand != "" {
			fitVal += " (" + rec.FitBand + ")"
		}
		addItem("Fit", fitVal)
	}

	// Recommendation.
	if rec.RecRank != nil {
		recVal := "#" + intStr(rec.RecRank)
		if rec.RecTier != "" {
			recVal += " tier " + rec.RecTier
		}
		addItem("Recommendation", recVal)
	}
	addItem("Rec Note", rec.RecNote)

	// Apply link — HTML:true with html.EscapeString on URL (not raw DB text direct in markup).
	if rec.URL != "" {
		applyHTML := `<a href="` + html.EscapeString(rec.URL) + `" target="_blank" rel="noopener noreferrer">Apply ↗</a>`
		items = append(items, resource.DetailItem{Label: "Apply", Value: applyHTML, HTML: true})
	}

	return resource.DetailSection{
		Title: "Overview",
		Items: items,
	}
}

// buildApplicationSectionHTML renders the rate form and download links via
// html/template (autoescape on all struct fields). The CSRF token is a
// hex/base64 value from csrf.Issue and is safe as a hidden form value.
func buildApplicationSectionHTML(id64 int64, csrfTok string, rating *currentRating) (string, error) {
	data := applicationSectionData{
		ID:        id64,
		CSRFToken: csrfTok,
		Rating:    rating,
	}
	var buf bytes.Buffer
	if err := applicationSectionTmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("applicationSectionTmpl.Execute: %w", err)
	}
	return buf.String(), nil
}

// isJobNotFound returns true when the row SELECT matched no job.
func isJobNotFound(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}

// salaryDetailStr formats the full salary string with currency and interval.
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

// jobDetailStyles is the CSS required for the detail page cards (fit/market/description).
// Injected as the first RawHTML section so it applies to all sections below.
const jobDetailStyles = `<style>
.md-body{background:#1e293b;border-radius:.5rem;padding:1.25rem;font-size:.9rem;line-height:1.65;color:#cbd5e1}
.md-body h2{font-size:1.1rem;margin:.75rem 0 .4rem;color:#e2e8f0}
.md-body h3{font-size:1rem;margin:.6rem 0 .3rem;color:#e2e8f0}
.md-body p{margin:.4rem 0}
.md-body ul,.md-body ol{padding-left:1.4em;margin:.4rem 0}
.md-body code{background:#334155;padding:.1em .35em;border-radius:.25rem;font-size:.85em}
.md-body pre{background:#334155;padding:.75rem 1rem;border-radius:.35rem;overflow-x:auto}
.dl-link{display:inline-block;margin-top:.75rem;margin-right:.75rem;padding:.45rem 1rem;background:#334155;color:#e2e8f0;border-radius:.35rem;text-decoration:none;font-size:.85rem}
.dl-link:hover{background:#475569}
.rate-form{margin-top:1rem}
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
.fit-card-cols{display:grid;grid-template-columns:1fr 1fr;gap:1rem;margin-top:.5rem}
.fit-col-label{font-size:.75rem;font-weight:600;color:#94a3b8;text-transform:uppercase;letter-spacing:.05em;margin-bottom:.5rem}
.fit-col-list{margin:0;padding-left:1.2em;font-size:.875rem;color:#cbd5e1}
.fit-col-list li{margin-bottom:.25rem}
.market-card{background:#1e293b;border-radius:.5rem;padding:1rem}
.market-card-header{display:flex;align-items:center;gap:.75rem;margin-bottom:.75rem}
.market-card-label{font-size:.75rem;font-weight:600;color:#94a3b8;text-transform:uppercase;letter-spacing:.05em}
.market-reasoning{font-size:.875rem;color:#cbd5e1;margin-bottom:.75rem}
.market-disclaimer{font-size:.75rem;color:#64748b;border-top:1px solid #334155;padding-top:.5rem;margin-top:.5rem}
.fit-chip{display:inline-flex;align-items:center;gap:.25rem;padding:.15rem .5rem;border-radius:.75rem;font-size:.8rem;font-weight:600}
.fit-strong{background:#14532d;color:#86efac}.fit-moderate{background:#1e3a5f;color:#93c5fd}
.fit-weak{background:#78350f;color:#fcd34d}.fit-low{background:#4c1d21;color:#fca5a5}
.fit-reject{background:#3b1c1c;color:#f87171}.fit-unscored{background:#1e293b;color:#64748b}
.suc-chip{display:inline-flex;align-items:center;gap:.25rem;padding:.15rem .5rem;border-radius:.75rem;font-size:.8rem;font-weight:600}
.suc-strong{background:#14532d;color:#86efac}.suc-moderate{background:#1e3a5f;color:#93c5fd}
.suc-longshot{background:#4c1d21;color:#fca5a5}.suc-none{color:#64748b}
.ou-glyph{font-size:.85rem}.ou-over{color:#fbbf24}.ou-match{color:#34d399}.ou-under{color:#f87171}
</style>`
