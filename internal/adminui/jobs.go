package adminui

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/anatolykoptev/go-kit/admintable"
	"github.com/anatolykoptev/go-panel/resource"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Column/filter keys reused across the sort spec and filter spec.
const (
	colStatus   = "status"
	colSource   = "source"
	colKeyTitle = "title"
	keyRecent   = "recent"
	colLastSeen = "last_seen_at"
	lblStatus   = "Status"
	lblRecent   = "Recent"
	lblSource   = "Source"
	lblPlatform = "Platform"
	grpHunt     = "Hunt"
	colPlatform = "platform"
	keyQ        = "q"
)

// Fit-band chip constants — CLOSED ENUM. Only these values are interpolated into
// HTML. Unknown bands fall through to the muted fallback (never raw DB text).
const (
	fitBandStrong   = "strong"
	fitBandModerate = "moderate"
	fitBandWeak     = "weak"
	fitBandLow      = "low"
	fitBandReject   = "reject"
	fitBandUnscored = "unscored"
	fitBandStale    = "stale"
)

// Success-band chip constants — CLOSED ENUM.
const (
	sucBandStrong   = "STRONG"
	sucBandModerate = "MODERATE"
	sucBandLongshot = "LONGSHOT"
)

// Over/under constants — CLOSED ENUM.
const (
	ouOver  = "over_qualified"
	ouMatch = "well_matched"
	ouUnder = "under_qualified"
)

// CSS class constants (goconst: used 4+ times across the chip helpers).
const (
	cssFitUnscored  = "fit-unscored"
	cssSucModerate  = "suc-moderate"
)

// Column label constants (goconst: "Posted" used across jobs + huntlists specs).
const lblPosted = "Posted"

// scoreRationale mirrors the JSONB shape in score_rationale.
// The struct is defined in internal/hunt/types.go but lives in package hunt.
// We redeclare a local anonymous shape to avoid a cross-package import.
type adminScoreRationale struct {
	FitReasons       []string `json:"fit_reasons"`
	FitGaps          []string `json:"fit_gaps"`
	SuccessReasoning string   `json:"success_reasoning"`
}

// jobsSpec drives the /admin/jobs table sort/columns. Cell order in the Lister
// MUST match Columns order. Ported from go-nerv's retired jobsSortSpec.
var jobsSpec = admintable.Spec{
	Columns: []admintable.Column{
		{Key: "fit", Label: "Fit", Sortable: true, SQLExpr: "fit_score", NullsLast: true, TieBreakSQLExpr: "last_seen_at DESC", Width: "8rem"},
		{Key: colKeyTitle, Label: "Title / Company", Sortable: true, SQLExpr: colKeyTitle},
		{Key: "market", Label: "Market Read", Sortable: true, SQLExpr: "CASE success_band WHEN 'STRONG' THEN 3 WHEN 'MODERATE' THEN 2 WHEN 'LONGSHOT' THEN 1 ELSE 0 END", NullsLast: true, Width: "11rem"},
		{Key: colStatus, Label: lblStatus, Sortable: true, SQLExpr: colStatus},
		{Key: "posted", Label: lblPosted, Sortable: true, SQLExpr: "posted_at", NullsLast: true, TieBreakSQLExpr: "last_seen_at DESC", Width: "6rem"},
		{Key: "location", Label: "Location", Sortable: false},
		{Key: colSource, Label: lblSource, Sortable: false, Width: "6rem"},
	},
	DefaultKey: "fit",
	DefaultDir: admintable.Desc,
}

// jobsFilter declares the /admin/jobs filter bar. Every SQLExpr is author-constant;
// request values reach SQL only as bind args (never concatenated). Allowed sets are
// safe-degrade (an unknown value drops the filter, never an error).
var jobsFilter = admintable.FilterSpec{Filters: []admintable.Filter{
	{Key: keyQ, SQLExprs: []string{colKeyTitle, "company"}, Match: admintable.ILike},
	{Key: colStatus, SQLExpr: colStatus, Match: admintable.Eq, Allowed: []string{"open", "applied", "interviewing", "rejected", "offer", "closed"}},
	{Key: colSource, SQLExpr: colSource, Match: admintable.Eq, Allowed: []string{"ashby", "greenhouse", "hn", "indeed", "lever", "yc"}},
}}

func jobsResource(pool *pgxpool.Pool) resource.Resource {
	return resource.Resource{
		Name:     "jobs",
		Title:    "Jobs",
		Icon:     "\U0001F4BC",
		Group:    "Hunt",
		Sort:     jobsSpec,
		Filter:   jobsFilter,
		Perms:    resource.ReadAny,
		Lister:   jobsLister(pool),
		Detailer: jobsDetailer(pool),
	}
}

func jobsLister(pool *pgxpool.Pool) func(context.Context, resource.ListQuery) ([]resource.Row, int, error) {
	return func(ctx context.Context, q resource.ListQuery) ([]resource.Row, int, error) {
		where := "TRUE"
		if strings.TrimSpace(q.WhereConds) != "" {
			where = q.WhereConds
		}
		var total int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM hunt_jobs WHERE "+where, q.WhereArgs...).Scan(&total); err != nil {
			return nil, 0, fmt.Errorf("adminui: count jobs: %w", err)
		}
		n := len(q.WhereArgs)
		args := append(append([]any{}, q.WhereArgs...), q.Limit, q.Offset)
		query := fmt.Sprintf(`
			SELECT id, COALESCE(title,''), COALESCE(company,''), COALESCE(status,''),
			       fit_score, COALESCE(fit_band,''), COALESCE(success_band,''), COALESCE(over_under,''),
			       posted_at, last_seen_at,
			       COALESCE(location,''), COALESCE(source,''), COALESCE(url,'')
			  FROM hunt_jobs WHERE %s ORDER BY %s LIMIT $%d OFFSET $%d`,
			where, jobsSpec.OrderBy(q.Sort), n+1, n+2)
		rows, err := pool.Query(ctx, query, args...)
		if err != nil {
			return nil, 0, fmt.Errorf("adminui: list jobs: %w", err)
		}
		defer rows.Close()

		var out []resource.Row
		for rows.Next() {
			var (
				id                     int64
				title, company, status string
				fitBand, sucBand, ou   string
				location, source, url  string
				fit                    *int
				posted, recent         *time.Time
			)
			if err := rows.Scan(&id, &title, &company, &status, &fit, &fitBand, &sucBand, &ou,
				&posted, &recent, &location, &source, &url); err != nil {
				return nil, 0, fmt.Errorf("adminui: scan job: %w", err)
			}
			titleCompany := title
			if company != "" {
				titleCompany = title + " · " + company
			}
			out = append(out, resource.Row{
				ID:   strconv.FormatInt(id, 10),
				Href: "/admin/jobs/" + strconv.FormatInt(id, 10) + "/view",
				Cells: []resource.Cell{
					{Value: fitChipHTML(fit, fitBand), HTML: true},
					{Value: titleCompany},
					{Value: marketReadHTML(sucBand, ou), HTML: true},
					{Value: status},
					{Value: dateStr(posted)},
					{Value: location},
					{Value: source},
				},
			})
		}
		return out, total, rows.Err()
	}
}

// fitChipHTML returns XSS-safe HTML for the Fit axis cell.
// fit may be nil (unscored/stale). band is one of the fitBand* constants.
// Only closed-enum CSS classes are interpolated; no raw DB text is used.
func fitChipHTML(fit *int, band string) string {
	// Unscored / stale / nil — show muted dash, never a fake score.
	if fit == nil || band == fitBandUnscored || band == fitBandStale {
		return `<span class="fit-chip fit-unscored" title="Not assessed — scorer needs a profile">—</span>`
	}

	// Map band → (CSS modifier, abbreviated label). Unknown band → muted fallback.
	type chipSpec struct{ cls, lbl string }
	specs := map[string]chipSpec{
		fitBandStrong:   {"fit-strong", "str"},
		fitBandModerate: {"fit-moderate", "mod"},
		fitBandWeak:     {"fit-weak", "wk"},
		fitBandLow:      {"fit-low", "low"},
		fitBandReject:   {"fit-reject", "rej"},
	}
	cs, ok := specs[band]
	if !ok {
		// Unknown band — muted fallback (never raw band text in HTML).
		cs = chipSpec{cssFitUnscored, "?"}
	}
	return fmt.Sprintf(
		`<span class="fit-chip %s"><span class="fit-num">%d</span> <span class="fit-label">%s</span></span>`,
		cs.cls, *fit, cs.lbl,
	)
}

// marketReadHTML returns XSS-safe HTML for the Market Read cell combining the
// success-band chip and the over/under glyph.
// All values (CSS class, glyph, label, title) come from closed-enum maps — no
// raw DB text reaches the HTML output. Band and ou from the DB are used only as
// map keys to select constants.
func marketReadHTML(band, ou string) string {
	if band == "" {
		return `<span class="suc-none" aria-label="Not assessed">—</span>`
	}

	type sucSpec struct{ cls, glyph, lbl string }
	sucSpecs := map[string]sucSpec{
		sucBandStrong:   {"suc-strong", "◆", "STRONG"},
		sucBandModerate: {cssSucModerate, "◇", "MOD"},
		sucBandLongshot: {"suc-longshot", "◈", "LONG"},
	}
	ss, ok := sucSpecs[band]
	if !ok {
		ss = sucSpec{cssSucModerate, "◇", "?"}
	}

	type ouSpec struct{ cls, glyph, title string }
	ouSpecs := map[string]ouSpec{
		ouOver:  {"ou-over", "↑", "Over-qualified for this level"},
		ouMatch: {"ou-match", "=", "Well matched"},
		ouUnder: {"ou-under", "↓", "Under-qualified / gap"},
	}
	os, ok := ouSpecs[ou]
	if !ok {
		os = ouSpec{"ou-match", "=", "Well matched"}
	}

	// Use the display label (from enum map) in the title — never the raw DB band string.
	chipTitle := html.EscapeString("Market Read: " + ss.lbl + " — LLM estimate of competitiveness only")
	ouTitle := html.EscapeString(os.title)
	return fmt.Sprintf(
		`<span style="display:inline-flex;align-items:center;gap:.375rem">`+
			`<span class="suc-chip %s" title="%s">%s %s</span>`+
			`<span class="ou-glyph %s" title="%s">%s</span>`+
			`</span>`,
		ss.cls, chipTitle, ss.glyph, ss.lbl,
		os.cls, ouTitle, os.glyph,
	)
}

// jobsDetailer returns the Detailer closure for the jobs resource. It loads
// one job by id (string) and builds the fit-card + market-read-card sections.
// XSS contract: fit_reasons / fit_gaps / success_reasoning are LLM free-text
// and go into DetailItem{HTML:false} so go-panel HTML-escapes them. Only
// closed-enum chip HTML (fitChipHTML / marketReadHTML) uses HTML:true or RawHTML.
func jobsDetailer(pool *pgxpool.Pool) func(context.Context, string) ([]resource.DetailSection, error) {
	return func(ctx context.Context, idStr string) ([]resource.DetailSection, error) {
		id64, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("adminui: detail jobs: bad id %q", idStr)
		}

		var (
			title, company, url, source, location string
			fitScore                               *int
			fitBand, sucBand, ou                  string
			postedAt                               *time.Time
			rationaleRaw                           []byte
		)
		const q = `
			SELECT COALESCE(title,''), COALESCE(company,''), COALESCE(url,''),
			       COALESCE(source,''), COALESCE(location,''),
			       fit_score, COALESCE(fit_band,''), COALESCE(success_band,''), COALESCE(over_under,''),
			       posted_at, score_rationale
			  FROM hunt_jobs WHERE id = $1`
		if err := pool.QueryRow(ctx, q, id64).Scan(
			&title, &company, &url, &source, &location,
			&fitScore, &fitBand, &sucBand, &ou,
			&postedAt, &rationaleRaw,
		); err != nil {
			return nil, fmt.Errorf("adminui: detail jobs fetch: %w", err)
		}

		// Parse score_rationale JSONB. Nil or malformed → empty rationale (honest "not assessed").
		var rat adminScoreRationale
		if len(rationaleRaw) > 0 {
			if err := json.Unmarshal(rationaleRaw, &rat); err != nil {
				slog.WarnContext(ctx, "adminui: detail jobs: malformed score_rationale, showing not-assessed",
					slog.Int64("job_id", id64), slog.Any("err", err))
				// Leave rat empty — detail renders "not assessed" cards.
			}
		}

		var sections []resource.DetailSection

		// ── Job header section ─────────────────────────────────────────────────
		headerItems := []resource.DetailItem{
			{Label: "Company", Value: company},
			{Label: "Source", Value: source},
			{Label: "Location", Value: location},
			{Label: "Posted", Value: dateStr(postedAt)},
			{Label: "Fit", Value: fitChipHTML(fitScore, fitBand), HTML: true},
		}
		if url != "" {
			headerItems = append(headerItems, resource.DetailItem{
				Label: "Posting",
				Value: `<a href="` + html.EscapeString(url) + `" target="_blank" rel="noopener noreferrer">Open ↗</a>`,
				HTML:  true,
			})
		}
		sections = append(sections, resource.DetailSection{
			Title: title,
			Items: filterEmpty(headerItems),
		})

		// ── FIT ASSESSMENT card ────────────────────────────────────────────────
		switch {
		case fitScore == nil || fitBand == fitBandUnscored || fitBand == fitBandStale:
			sections = append(sections, resource.DetailSection{
				Title: "FIT ASSESSMENT",
				Items: []resource.DetailItem{
					{Label: "Status", Value: "Not yet assessed — scorer ran without profile or job is stale"},
				},
			})
		case fitBand == fitBandReject:
			sections = append(sections, resource.DetailSection{
				Title: "FIT ASSESSMENT",
				Items: []resource.DetailItem{
					{Label: "Status", Value: "Rejected by keyword pre-filter (Jaccard) — no LLM scoring"},
				},
			})
		default:
			// Build the two-column fit-card as RawHTML (only free-text via html.EscapeString).
			sections = append(sections, resource.DetailSection{
				Title:   "FIT ASSESSMENT",
				RawHTML: buildFitCardHTML(rat.FitReasons, rat.FitGaps),
			})
		}

		// ── MARKET READ card ───────────────────────────────────────────────────
		sections = append(sections, resource.DetailSection{
			Title:   "MARKET READ",
			RawHTML: buildMarketCardHTML(sucBand, ou, rat.SuccessReasoning),
		})

		return sections, nil
	}
}

// buildFitCardHTML renders the two-column WHY YOU / GAPS layout.
// fit_reasons and fit_gaps are LLM free-text — every item is escaped via
// html.EscapeString before being placed in HTML. No raw text enters as markup.
func buildFitCardHTML(reasons, gaps []string) string {
	var b strings.Builder
	b.WriteString(`<div class="fit-card-cols">`)

	// WHY YOU column.
	b.WriteString(`<div><div class="fit-col-label">WHY YOU</div><ul class="fit-col-list">`)
	if len(reasons) == 0 {
		b.WriteString(`<li>—</li>`)
	} else {
		for _, r := range reasons {
			b.WriteString(`<li>` + html.EscapeString(r) + `</li>`)
		}
	}
	b.WriteString(`</ul></div>`)

	// GAPS column.
	b.WriteString(`<div><div class="fit-col-label">GAPS</div><ul class="fit-col-list">`)
	if len(gaps) == 0 {
		b.WriteString(`<li>—</li>`)
	} else {
		for _, g := range gaps {
			b.WriteString(`<li>` + html.EscapeString(g) + `</li>`)
		}
	}
	b.WriteString(`</ul></div>`)

	b.WriteString(`</div>`)
	return b.String()
}

// buildMarketCardHTML renders the MARKET READ card with the success chip,
// over/under glyph, prose reasoning, and the mandatory honesty disclaimer.
// success_reasoning is LLM free-text — escaped via html.EscapeString.
// Band/ou values are used only as map keys → chip CSS (closed enum).
func buildMarketCardHTML(band, ou, reasoning string) string {
	var b strings.Builder

	b.WriteString(`<div class="market-card">`)

	// Header row: MARKET READ label + success chip + over/under glyph.
	b.WriteString(`<div class="market-card-header">`)
	b.WriteString(`<span class="market-card-label">MARKET READ</span>`)
	b.WriteString(marketReadHTML(band, ou))
	b.WriteString(`</div>`)

	// Reasoning prose — LLM free-text, must be escaped.
	if reasoning != "" {
		b.WriteString(`<div class="market-reasoning">` + html.EscapeString(reasoning) + `</div>`)
	} else if band == "" {
		b.WriteString(`<div class="market-reasoning" style="color:var(--text-muted)">Not assessed — scorer ran without profile.</div>`)
	}

	// Honesty disclaimer — ALWAYS rendered, non-negotiable per spec §3c.
	b.WriteString(`<div class="market-disclaimer">LLM heuristic — competitiveness estimate only. Not a probability. Based on candidate profile vs. job description; applicant pool is unobservable.</div>`)

	b.WriteString(`</div>`)
	return b.String()
}

// filterEmpty removes DetailItems whose Value is empty.
func filterEmpty(items []resource.DetailItem) []resource.DetailItem {
	out := make([]resource.DetailItem, 0, len(items))
	for _, it := range items {
		if it.Value != "" {
			out = append(out, it)
		}
	}
	return out
}

func intStr(p *int) string {
	if p == nil {
		return ""
	}
	return strconv.Itoa(*p)
}

func dateStr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format("2006-01-02")
}

func salaryStr(lo, hi *int) string {
	switch {
	case lo == nil && hi == nil:
		return ""
	case lo != nil && hi != nil:
		return fmt.Sprintf("%d–%d", *lo, *hi)
	case lo != nil:
		return fmt.Sprintf("%d+", *lo)
	default:
		return fmt.Sprintf("≤%d", *hi)
	}
}
