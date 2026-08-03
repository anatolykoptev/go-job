package adminui

// huntsettings.go — operator-tunable hunt worker settings page (GET /admin/settings,
// POST /admin/settings/save). Reads/writes the single-row hunt_settings table
// via hunt.Store.GetHuntSettings / SaveHuntSettings. Env vars remain as fallback
// defaults when a DB field is zero/unset — see huntworker.LoadSettings.

import (
	"bytes"
	"context"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/anatolykoptev/go-panel/auth"
	"github.com/anatolykoptev/go-panel/csrf"
	"github.com/anatolykoptev/go-panel/resource"
	"github.com/anatolykoptev/go_job/internal/hunt"
)

// navIDSettings is the sidebar nav ID for the hunt settings page.
const navIDSettings = "settings"

// huntSettingsStore is the narrow interface for loading/saving hunt settings.
// Implemented by *hunt.Store.
type huntSettingsStore interface {
	GetHuntSettings(ctx context.Context) (hunt.HuntSettings, error)
	SaveHuntSettings(ctx context.Context, settings hunt.HuntSettings) error
}

// huntSettingsPageData is the template context for /admin/settings.
type huntSettingsPageData struct {
	CSRFToken string
	S         hunt.HuntSettings
	// Formatted duration fields for display (e.g. "6h", "48h", "10m").
	IntervalStr     string
	NotifyMaxAgeStr string
	// Error/success flash messages.
	ErrMsg string
	OKMsg  string
}

// huntSettingsHandler serves GET /admin/settings — displays the settings form.
func huntSettingsHandler(p *resource.Panel, store huntSettingsStore, a auth.Authenticator, csrfKey []byte) http.HandlerFunc {
	tmpl := template.Must(template.New("hunt_settings").Parse(huntSettingsTmplSrc))

	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		s, err := store.GetHuntSettings(ctx)
		if err != nil {
			slog.Error("huntSettingsHandler: GetHuntSettings", "err", err)
			s = hunt.HuntSettings{} // show defaults on error
		}

		sessVal := sessionValue(r, a.(cookieNamer).SessionCookieName())
		d := huntSettingsPageData{
			CSRFToken:       csrf.Issue(csrfKey, sessVal, csrf.DefaultTTL),
			S:               s,
			IntervalStr:     formatDuration(s.Interval),
			NotifyMaxAgeStr: formatDuration(s.NotifyMaxAge),
		}

		var buf bytes.Buffer
		if execErr := tmpl.Execute(&buf, d); execErr != nil {
			slog.Error("huntSettingsHandler: template execute", "err", execErr)
			http.Error(w, "render error", http.StatusInternalServerError)
			return
		}
		if err := p.RenderPageHTML(w, r, "Hunt Settings", navIDSettings, buf.String()); err != nil {
			slog.Error("adminui: render hunt_settings", "err", err)
		}
	}
}

// huntSettingsSaveHandler handles POST /admin/settings/save.
// CSRF is verified by MountAction before this handler runs.
func huntSettingsSaveHandler(store huntSettingsStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		var s hunt.HuntSettings

		// Parse bool fields (checkbox → "on" = true, absent = false).
		s.Enabled = r.FormValue("enabled") == "on"
		s.ScoreEnabled = r.FormValue("score_enabled") == "on"
		s.ScoreFailOpen = r.FormValue("score_fail_open") == "on"

		// Parse text field.
		s.Queries = strings.TrimSpace(r.FormValue("queries"))

		// Parse int fields.
		s.NotifyChatID = parseInt64(r.FormValue("notify_chat_id"))
		s.NotifyMinFit = parseIntClamped(r.FormValue("notify_min_fit"), 0, 100)
		s.ScoreMinJaccard = parseIntDefault(r.FormValue("score_min_jaccard"), 8)
		s.ScoreMaxLLMPerCycle = parseIntDefault(r.FormValue("score_max_llm_per_cycle"), 50)
		s.ScoreSweepLimit = parseIntDefault(r.FormValue("score_sweep_limit"), 50)

		// Parse duration fields.
		s.Interval = parseDurationDefault(r.FormValue("interval"), 6*time.Hour)
		s.NotifyMaxAge = parseDurationDefault(r.FormValue("notify_max_age"), 48*time.Hour)

		if err := store.SaveHuntSettings(ctx, s); err != nil {
			slog.Error("huntSettingsSaveHandler: SaveHuntSettings", "err", err)
			http.Error(w, "save failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		slog.Info("adminui: hunt settings saved", "enabled", s.Enabled, "queries", s.Queries)
		http.Redirect(w, r, "/admin/settings?ok=1", http.StatusSeeOther)
	}
}

// formatDuration renders a time.Duration as a short Go-style string (e.g. "6h", "48h", "10m").
// Zero duration → "".
func formatDuration(d time.Duration) string {
	if d == 0 {
		return ""
	}
	return d.String()
}

// parseInt64 parses a string as int64, returning 0 on error.
func parseInt64(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}

// parseIntDefault parses a string as int, returning def on error or empty.
func parseIntDefault(s string, def int) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// parseIntClamped parses a string as int, clamping to [min,max], returning min on error.
func parseIntClamped(s string, min, max int) int {
	n := parseIntDefault(s, min)
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}

// parseDurationDefault parses a Go duration string (e.g. "6h", "48h", "10m"),
// returning def on error or empty.
func parseDurationDefault(s string, def time.Duration) time.Duration {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return def
	}
	return d
}

// huntSettingsTmplSrc is the HTML content fragment for the hunt settings page.
const huntSettingsTmplSrc = `<style>
  .hs-form{max-width:720px}
  .hs-section{background:var(--bg-surface,#1e293b);border:1px solid var(--border,#334155);border-radius:var(--radius-lg,.75rem);padding:1.25rem 1.5rem;margin-bottom:1.25rem}
  .hs-section h3{font-size:.9375rem;font-weight:700;color:var(--text-primary,#f1f5f9);margin-bottom:.75rem;display:flex;align-items:center;gap:.5rem}
  .hs-field{margin-bottom:1rem}
  .hs-field:last-child{margin-bottom:0}
  .hs-label{display:block;font-size:.8125rem;font-weight:600;color:var(--text-secondary,#94a3b8);margin-bottom:.25rem}
  .hs-hint{font-size:.75rem;color:var(--text-muted,#64748b);margin-top:.25rem}
  .hs-input{width:100%;padding:.5rem .75rem;background:var(--bg-elevated,#0f172a);border:1px solid var(--border,#334155);border-radius:var(--radius-md,.375rem);color:var(--text-primary,#f1f5f9);font-size:.875rem;font-family:var(--font-mono,monospace)}
  .hs-input:focus{outline:none;border-color:var(--accent,#60a5ba)}
  .hs-checkbox{display:flex;align-items:center;gap:.5rem}
  .hs-checkbox input{width:1.125rem;height:1.125rem;accent-color:var(--accent,#60a5fa)}
  .hs-checkbox label{font-size:.875rem;color:var(--text-primary,#f1f5f9);cursor:pointer}
  .hs-actions{display:flex;gap:.75rem;align-items:center;margin-top:1.5rem}
  .hs-btn{padding:.5rem 1.25rem;background:var(--accent,#60a5fa);color:#0f172a;border:none;border-radius:var(--radius-md,.375rem);font-size:.875rem;font-weight:600;cursor:pointer}
  .hs-btn:hover{opacity:.9}
  .hs-flash-ok{padding:.75rem 1rem;background:rgba(34,197,94,.15);border:1px solid rgba(34,197,94,.4);border-radius:var(--radius-md,.375rem);color:#4ade80;font-size:.8125rem;margin-bottom:1rem}
</style>

<div class="page-header">
  <h2>&#9881; Hunt Settings</h2>
  <p>Operator-tunable configuration for the automated job ingest worker. Changes apply on the next hunt cycle (no redeploy needed).</p>
</div>

{{if .OKMsg}}<div class="hs-flash-ok">{{.OKMsg}}</div>{{end}}

<form method="POST" action="/admin/settings/save" class="hs-form">
  <input type="hidden" name="csrf" value="{{.CSRFToken}}">

  <div class="hs-section">
    <h3>&#128229; Ingest</h3>
    <div class="hs-field hs-checkbox">
      <input type="checkbox" id="enabled" name="enabled" {{if .S.Enabled}}checked{{end}}>
      <label for="enabled">Enabled — run the hunt worker on a schedule</label>
    </div>
    <div class="hs-field">
      <label class="hs-label" for="interval">Cycle interval (Go duration)</label>
      <input class="hs-input" type="text" id="interval" name="interval" value="{{.IntervalStr}}" placeholder="6h">
      <div class="hs-hint">How often to run a full ingest cycle. Examples: 6h, 1h, 30m. Applied at worker start (requires restart to change the ticker).</div>
    </div>
    <div class="hs-field">
      <label class="hs-label" for="queries">Search queries (comma-separated)</label>
      <input class="hs-input" type="text" id="queries" name="queries" value="{{.S.Queries}}">
      <div class="hs-hint">Generic role/skill strings. No company names (public repo). Applied per-cycle.</div>
    </div>
  </div>

  <div class="hs-section">
    <h3>&#128172; Telegram Notify</h3>
    <div class="hs-field">
      <label class="hs-label" for="notify_chat_id">Chat ID</label>
      <input class="hs-input" type="text" id="notify_chat_id" name="notify_chat_id" value="{{.S.NotifyChatID}}">
      <div class="hs-hint">Telegram chat ID to send notifications to. 0 = no Telegram notify (uses env fallback).</div>
    </div>
    <div class="hs-field">
      <label class="hs-label" for="notify_min_fit">Minimum fit score (0-100)</label>
      <input class="hs-input" type="text" id="notify_min_fit" name="notify_min_fit" value="{{.S.NotifyMinFit}}">
      <div class="hs-hint">Only notify jobs with fit_score ≥ this. 0 = gate open (all scores pass). Applied per-cycle.</div>
    </div>
    <div class="hs-field">
      <label class="hs-label" for="notify_max_age">Max job age (Go duration)</label>
      <input class="hs-input" type="text" id="notify_max_age" name="notify_max_age" value="{{.NotifyMaxAgeStr}}" placeholder="48h">
      <div class="hs-hint">Recency gate — jobs posted older than this → stale (no LLM). Applied per-cycle.</div>
    </div>
  </div>

  <div class="hs-section">
    <h3>&#129504; LLM Scoring</h3>
    <div class="hs-field hs-checkbox">
      <input type="checkbox" id="score_enabled" name="score_enabled" {{if .S.ScoreEnabled}}checked{{end}}>
      <label for="score_enabled">Scoring enabled — use LLM to score job fit</label>
    </div>
    <div class="hs-field hs-checkbox">
      <input type="checkbox" id="score_fail_open" name="score_fail_open" {{if .S.ScoreFailOpen}}checked{{end}}>
      <label for="score_fail_open">Fail-open — notify with degraded card on LLM error (unchecked = drop)</label>
    </div>
    <div class="hs-field">
      <label class="hs-label" for="score_min_jaccard">Min Jaccard (0-100)</label>
      <input class="hs-input" type="text" id="score_min_jaccard" name="score_min_jaccard" value="{{.S.ScoreMinJaccard}}">
      <div class="hs-hint">Pre-filter threshold; below → reject without LLM. Applied per-cycle.</div>
    </div>
    <div class="hs-field">
      <label class="hs-label" for="score_max_llm_per_cycle">Max LLM calls per cycle</label>
      <input class="hs-input" type="text" id="score_max_llm_per_cycle" name="score_max_llm_per_cycle" value="{{.S.ScoreMaxLLMPerCycle}}">
      <div class="hs-hint">Per-cycle LLM budget ceiling (circuit breaker). Applied per-cycle.</div>
    </div>
    <div class="hs-field">
      <label class="hs-label" for="score_sweep_limit">Sweep limit</label>
      <input class="hs-input" type="text" id="score_sweep_limit" name="score_sweep_limit" value="{{.S.ScoreSweepLimit}}">
      <div class="hs-hint">Max unscored-open jobs to backfill per cycle. Applied per-cycle.</div>
    </div>
  </div>

  <div class="hs-actions">
    <button type="submit" class="hs-btn">Save Settings</button>
  </div>
</form>`
