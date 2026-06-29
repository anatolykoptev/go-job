package adminui

import (
	"context"
	"fmt"
	"html"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/anatolykoptev/go-kit/admintable"
	"github.com/anatolykoptev/go-panel/resource"
	"github.com/anatolykoptev/go-panel/shell"
	"github.com/anatolykoptev/go_job/internal/engine/jobs/applications"
	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Column/filter keys reused across the sort spec and filter spec.
const (
	colStatus   = "status"
	colSource   = "source"
	colKeyTitle = "title"
	colKeyFit   = "fit"
	keyRecent   = "recent"
	colLastSeen = "last_seen_at"
	colCompany  = "company"
	lblStatus   = "Status"
	lblTitle    = "Title"
	lblRecent   = "Recent"
	lblSource   = "Source"
	lblPlatform = "Platform"
	grpHunt     = "Hunt"
	colPlatform = "platform"
	keyQ        = "q"
)

// colWidth8rem is the shared column width used for compact chip columns.
const colWidth8rem = "8rem"

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
	cssFitUnscored = "fit-unscored"
	cssSucModerate = "suc-moderate"
)

// Column label constants (goconst: "Posted" used across jobs + huntlists specs).
const lblPosted = "Posted"

// SQL column reference constants for aliased queries (goconst: used 4+ times
// across jobsSpec, jobsFilter, jobsLister, and shortlistSpec/shortlistFilter).
const (
	sqlJTitle   = "j.title"
	sqlJCompany = "j.company"
	sqlJStatus  = "j.status"
	sqlJSource  = "j.source"
)

// Job status value constants (goconst: "open"/"closed" used across jobs + huntlists filter specs).
const (
	statusOpen   = "open"
	statusClosed = "closed"
)

// jobStatusFilterAllowed is the allowed set for the j.status filter bar.
// Derived from hunt.AllStatuses — the canonical enum for hunt_jobs.status.
// Only these values exist in the status column; pipeline-stage names
// (applied/rejected/offer/interviewing) were incorrectly placed here before
// ADR-003 clarified the two-plane model.
var jobStatusFilterAllowed = hunt.AllStatuses

// colKeyStar is the column key for the shortlist-star toggle column.
// Shared between jobsSpec and shortlistSpec (goconst: 4+ occurrences).
const colKeyStar = "star"

// colKeyStage is the column/filter key for the pipeline stage dropdown.
// Kept as a constant to avoid goconst warnings (used in jobsSpec + jobsFilter).
const colKeyStage = "stage"

// colKeyTriage is the filter key for the triage axis (hunt_ratings.triage).
// Shared with shortlist.go and stage_optgroup.go (goconst: 3+ occurrences).
const colKeyTriage = "triage"

// colWidthStage is the column width for the inline pipeline stage dropdown.
const colWidthStage = "9rem"

// sqlRStage is the SQL expression for the joined hunt_ratings.stage column.
const sqlRStage = "r.stage"

// sqlRTriage is the SQL expression for the joined hunt_ratings.triage column.
const sqlRTriage = "r.triage"

// jobsSpec drives the /admin/jobs table sort/columns. Cell order in the Lister
// MUST match Columns order.
//
// IMPORTANT: cell index 0 is the Href-linked cell in go-panel's list template.
// The template wraps cell-0 in <a href=…>{EscapeString(value)}</a> and ignores
// cell.HTML for that index. Therefore cell-0 MUST be plain text (Title).
// The star column is at index 1 — immediately after Title — so it appears at
// the front in practice without displacing the Href-linked cell-0.
// Stage dropdown is at index 2 (pipeline stage — NOT the job posting status).
// Fit and Market Read chips are at indices 4 and 5 (i>0 → cell.HTML respected).
var jobsSpec = admintable.Spec{
	Columns: []admintable.Column{
		{Key: colKeyTitle, Label: lblTitle, Sortable: true, SQLExpr: sqlJTitle},
		// Star toggle at index 1 (front of visible columns after Title).
		// Cell value is raw HTML (<form> with CSRF) — rendered with HTML: true.
		// Not sortable: star state is a join expression, not a table column.
		{Key: colKeyStar, Label: "★", Sortable: false, Width: "3rem"},
		// Stage dropdown at index 2. Inline <form> — POSTs to /admin/jobs/{id}/stage.
		// NOT the same as colStatus ("status" = job posting open/closed — separate axis).
		// Sortable via r.stage so operator can sort by pipeline funnel.
		{Key: colKeyStage, Label: "Stage", Sortable: true, SQLExpr: sqlRStage, NullsLast: true, Width: colWidthStage},
		{Key: colCompany, Label: "Company", Sortable: true, SQLExpr: sqlJCompany},
		{Key: colKeyFit, Label: "Fit", Sortable: true, SQLExpr: "j.fit_score", NullsLast: true, TieBreakSQLExpr: "j.last_seen_at DESC", Width: colWidth8rem},
		{Key: "market", Label: "Market Read", Sortable: true, SQLExpr: "CASE j.success_band WHEN 'STRONG' THEN 3 WHEN 'MODERATE' THEN 2 WHEN 'LONGSHOT' THEN 1 ELSE 0 END", NullsLast: true, Width: "11rem"},
		{Key: colStatus, Label: lblStatus, Sortable: true, SQLExpr: sqlJStatus},
		{Key: "posted", Label: lblPosted, Sortable: true, SQLExpr: "j.posted_at", NullsLast: true, TieBreakSQLExpr: "j.last_seen_at DESC", Width: "6rem"},
		{Key: "location", Label: "Location", Sortable: false},
		{Key: colSource, Label: lblSource, Sortable: false, Width: "6rem"},
		{Key: "docs", Label: "Docs", Sortable: false, Width: colWidth8rem},
	},
	DefaultKey: colKeyFit,
	DefaultDir: admintable.Desc,
}

// jobsFilter declares the /admin/jobs filter bar. Every SQLExpr is author-constant;
// request values reach SQL only as bind args (never concatenated). Allowed sets are
// safe-degrade (an unknown value drops the filter, never an error).
//
// After migration 012 the filter has TWO rating-axis entries:
//   - triage: filters on r.triage (interest signal), Allowed = TriageStages
//   - stage:  filters on r.stage  (pipeline position), Allowed = PipelineStages
var jobsFilter = admintable.FilterSpec{Filters: []admintable.Filter{
	{Key: keyQ, SQLExprs: []string{sqlJTitle, sqlJCompany}, Match: admintable.ILike},
	{Key: colStatus, SQLExpr: sqlJStatus, Match: admintable.Eq, Allowed: jobStatusFilterAllowed},
	{Key: colSource, SQLExpr: sqlJSource, Match: admintable.Eq, Allowed: []string{"ashby", "greenhouse", "hn", "indeed", "lever", "yc"}},
	// Triage filter on r.triage — works because jobsLister always LEFT JOINs hunt_ratings.
	{Key: colKeyTriage, SQLExpr: sqlRTriage, Match: admintable.Eq, Allowed: hunt.TriageStages},
	// Pipeline stage filter on r.stage — pipeline values only after migration 012.
	{Key: colKeyStage, SQLExpr: sqlRStage, Match: admintable.Eq, Allowed: hunt.PipelineStages},
}}

func jobsResource(store *hunt.Store, adminUser string, authority *applications.Authority, csrfKey []byte) resource.Resource {
	pool := store.Pool()
	return resource.Resource{
		Name:   "jobs",
		Title:  "Jobs",
		Icon:   "\U0001F4BC",
		Group:  grpHunt,
		Sort:   jobsSpec,
		Filter: jobsFilter,
		Perms:  resource.ReadAny,
		Badge: shell.CachedBadge(30*time.Second, func(ctx context.Context) string {
			n := store.CountOpenJobs(ctx)
			if n == 0 {
				return ""
			}
			return strconv.Itoa(n)
		}),
		Lister: jobsLister(pool, adminUser, authority, csrfKey),
		// Detailer wired in adminui.New: GET /admin/jobs/{id} served by go-panel framework.
	}
}

func jobsLister(pool *pgxpool.Pool, adminUser string, authority *applications.Authority, csrfKey []byte) func(context.Context, resource.ListQuery) ([]resource.Row, int, error) {
	return func(ctx context.Context, q resource.ListQuery) ([]resource.Row, int, error) {
		where := "TRUE"
		if strings.TrimSpace(q.WhereConds) != "" {
			where = q.WhereConds
		}
		// Count also uses the LEFT JOIN so that stage filter (on r.stage) works correctly.
		// Args layout for count: [...whereArgs, adminUser]
		n := len(q.WhereArgs)
		countArgs := append(append([]any{}, q.WhereArgs...), adminUser)
		var total int
		if err := pool.QueryRow(ctx,
			fmt.Sprintf(`SELECT count(*) FROM hunt_jobs j
				LEFT JOIN hunt_ratings r ON r.entry_kind = 'job' AND r.entry_id = j.id AND r.user_name = $%d
				WHERE %s`, n+1, where),
			countArgs...,
		).Scan(&total); err != nil {
			return nil, 0, fmt.Errorf("adminui: count jobs: %w", err)
		}

		// Args layout: [...whereArgs, adminUser, triageValues[], stageValues[], limit, offset]
		// $n+1 = adminUser, $n+2 = shortlistTriageValues, $n+3 = shortlistPipelineValues,
		// $n+4 = limit, $n+5 = offset.
		// The LEFT JOIN computes starred (bool), triage, and stage per-row from hunt_ratings.
		// All three reuse the same single LEFT JOIN — no second join.
		args := append(append([]any{}, q.WhereArgs...), adminUser, shortlistTriageValues, shortlistPipelineValues, q.Limit, q.Offset)
		query := fmt.Sprintf(`
			SELECT j.id, COALESCE(j.title,''), COALESCE(j.company,''), COALESCE(j.status,''),
			       j.fit_score, COALESCE(j.fit_band,''), COALESCE(j.success_band,''), COALESCE(j.over_under,''),
			       j.posted_at, j.last_seen_at,
			       COALESCE(j.location,''), COALESCE(j.source,''), COALESCE(j.url,''),
			       COALESCE(r.triage = ANY($%d::text[]) OR r.stage = ANY($%d::text[]), false) AS starred,
			       COALESCE(r.stage, '') AS stage
			  FROM hunt_jobs j
			  LEFT JOIN hunt_ratings r
			         ON r.entry_kind = 'job' AND r.entry_id = j.id AND r.user_name = $%d
			 WHERE %s ORDER BY %s LIMIT $%d OFFSET $%d`,
			n+2, n+3, n+1, where, jobsSpec.OrderBy(q.Sort), n+4, n+5)
		rows, err := pool.Query(ctx, query, args...)
		if err != nil {
			return nil, 0, fmt.Errorf("adminui: list jobs: %w", err)
		}
		defer rows.Close()

		// Snapshot legacy-dir entries once per list call to avoid N+1 ReadDir calls.
		var legacyEntries []os.DirEntry
		if authority != nil {
			legacyEntries = authority.LegacyEntries()
		}

		// Mint a single CSRF token for all star-toggle forms on this page.
		csrfTok := mintStarCSRF(ctx, csrfKey)

		var out []resource.Row
		for rows.Next() {
			var (
				id                           int64
				title, company, status       string
				fitBand, sucBand, ou         string
				location, source, url, stage string
				fit                          *int
				posted, recent               *time.Time
				starred                      bool
			)
			if err := rows.Scan(&id, &title, &company, &status, &fit, &fitBand, &sucBand, &ou,
				&posted, &recent, &location, &source, &url, &starred, &stage); err != nil {
				return nil, 0, fmt.Errorf("adminui: scan job: %w", err)
			}

			hasResume, hasCover := false, false
			if authority != nil {
				hasResume = authority.Exists(id, applications.KindResume)
				hasCover = authority.Exists(id, applications.KindCover)
				if !hasResume {
					hasResume = authority.LegacyExistsFromEntries(legacyEntries, company, title, applications.KindResume)
				}
				if !hasCover {
					hasCover = authority.LegacyExistsFromEntries(legacyEntries, company, title, applications.KindCover)
				}
			}

			// Cell order MUST match jobsSpec.Columns order.
			// Cell-0 = Title (plain text — go-panel wraps cell-0 in <a href>, ignoring
			// cell.HTML). Star is at cell-1 (front after Title). Stage dropdown at cell-2.
			// Company at cell-3. HTML: true cells at i>0 are rendered with raw HTML.
			// Row.Href → /admin/jobs/{id} (go-panel Detailer, natural URL).
			out = append(out, resource.Row{
				ID:   strconv.FormatInt(id, 10),
				Href: "/admin/jobs/" + strconv.FormatInt(id, 10),
				Cells: []resource.Cell{
					{Value: title},                                                         // [0] Title (plain text — Href-linked)
					{Value: starToggleHTML(id, starred, csrfTok), HTML: true},             // [1] Star (front after Title)
					{Value: stageDropdownHTML(id, stage, csrfTok), HTML: true},            // [2] Stage dropdown (pipeline stage, NOT job posting status)
					{Value: company},                                                       // [3] Company
					{Value: fitChipHTML(fit, fitBand), HTML: true},                        // [4] Fit chip
					{Value: marketReadHTML(sucBand, ou), HTML: true},                      // [5] Market chip
					{Value: status},                                                        // [6] Status (job posting open/closed — separate axis from stage)
					{Value: dateStr(posted)},                                               // [7] Posted
					{Value: location},                                                      // [8] Location
					{Value: source},                                                        // [9] Source
					{Value: docsChipHTML(id, hasResume, hasCover), HTML: true},            // [10] Docs
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
