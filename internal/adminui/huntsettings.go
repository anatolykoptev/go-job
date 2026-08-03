package adminui

// huntsettings.go — operator-tunable hunt worker settings, exposed as a
// go-panel resource.Resource with SingleRow=true (the canonical pattern for
// a single-row settings/profile resource — see upworkOverviewResource).
//
// The underlying store is the single-row hunt_settings table, accessed via
// hunt.Store.GetHuntSettings / SaveHuntSettings. Env vars remain as fallback
// defaults when a DB field is zero/unset — see huntworker.LoadSettings.

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/anatolykoptev/go-kit/admintable"
	"github.com/anatolykoptev/go-panel/resource"
	"github.com/anatolykoptev/go-panel/tenant"
	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/jackc/pgx/v5/pgxpool"
)

// grpHunt is the sidebar group label for hunt-related resources.
// (Defined in adminui.go; re-declared here only if not already — but Go
// allows same-package const reuse, so we reference the existing one.)

// huntSettingsResourceSpec is the admintable spec for the single-row list view.
// SingleRow resources still need a Lister (to find the row ID for the redirect).
var huntSettingsResourceSpec = admintable.Spec{
	Columns: []admintable.Column{
		{Key: "enabled", Label: "Enabled", Sortable: false, SQLExpr: "enabled::text", Width: "6rem"},
		{Key: "queries", Label: "Queries", Sortable: false, SQLExpr: "queries"},
		{Key: "interval", Label: "Interval", Sortable: false, SQLExpr: "interval::text", Width: "8rem"},
	},
}

// huntSettingsResource returns a go-panel resource.Resource for the single-row
// hunt_settings table. Registered via resource.Register in adminui.go.
func huntSettingsResource(pool *pgxpool.Pool) resource.Resource {
	store := hunt.NewStore(pool)
	return resource.Resource{
		Name:      "hunt_settings",
		Title:     "Hunt Settings",
		Icon:      "⚙",
		Group:     grpHunt,
		Sort:      huntSettingsResourceSpec,
		Filter:    admintable.FilterSpec{},
		SingleRow: true,
		Lister:    huntSettingsLister(store),
		FetchRow: func(ctx context.Context, _ string) (map[string]string, error) {
			s, err := store.GetHuntSettings(ctx)
			if err != nil {
				return nil, err
			}
			return huntSettingsToMap(s), nil
		},
		Writer: &resource.Writer{
			Form: resource.FormSpec{Fields: []resource.Field{
				{Key: "enabled", Label: "Ingest Enabled", Kind: resource.FieldCheckbox, Help: "Run the hunt worker on a schedule"},
				{Key: "interval", Label: "Cycle Interval (Go duration)", Kind: resource.FieldText, Help: "e.g. 6h, 1h, 30m. Applied at worker start (requires restart to change the ticker)."},
				{Key: "queries", Label: "Search Queries (comma-separated)", Kind: resource.FieldText, Help: "Generic role/skill strings. No company names (public repo). Applied per-cycle."},
				{Key: "notify_chat_id", Label: "Telegram Chat ID", Kind: resource.FieldText, Help: "0 = no Telegram notify (uses env fallback)."},
				{Key: "notify_min_fit", Label: "Min Fit Score (0-100)", Kind: resource.FieldNumber, Help: "Only notify jobs with fit_score ≥ this. 0 = gate open. Applied per-cycle."},
				{Key: "notify_max_age", Label: "Max Job Age (Go duration)", Kind: resource.FieldText, Help: "Recency gate — jobs posted older than this → stale (no LLM). e.g. 48h. Applied per-cycle."},
				{Key: "score_enabled", Label: "Scoring Enabled", Kind: resource.FieldCheckbox, Help: "Use LLM to score job fit. Applied per-cycle."},
				{Key: "score_fail_open", Label: "Fail-Open on LLM Error", Kind: resource.FieldCheckbox, Help: "Notify with degraded card on LLM error (unchecked = drop). Default false (#167)."},
				{Key: "score_min_jaccard", Label: "Min Jaccard (0-100)", Kind: resource.FieldNumber, Help: "Pre-filter threshold; below → reject without LLM. Applied per-cycle."},
				{Key: "score_max_llm_per_cycle", Label: "Max LLM Calls per Cycle", Kind: resource.FieldNumber, Help: "Per-cycle LLM budget ceiling (circuit breaker). Applied per-cycle."},
				{Key: "score_sweep_limit", Label: "Sweep Limit", Kind: resource.FieldNumber, Help: "Max unscored-open jobs to backfill per cycle. Applied per-cycle."},
			}},
			Load: func(ctx context.Context, _ tenant.Tenant, _ string) (map[string]string, error) {
				s, err := store.GetHuntSettings(ctx)
				if err != nil {
					return nil, err
				}
				return huntSettingsToMap(s), nil
			},
			Save: func(ctx context.Context, _ tenant.Tenant, _ string, v map[string]string) error {
				s := huntSettingsFromForm(v)
				if err := store.SaveHuntSettings(ctx, s); err != nil {
					return resource.NewSaveError("queries", err.Error())
				}
				return nil
			},
			RedirectAfterSave: func(_ context.Context, _ string) string { return "/admin/hunt_settings" },
		},
	}
}

// huntSettingsLister returns a single-row lister for the hunt_settings resource.
// SingleRow resources use the lister only to find the row ID for the redirect.
func huntSettingsLister(store *hunt.Store) func(context.Context, resource.ListQuery) ([]resource.Row, int, error) {
	return func(ctx context.Context, _ resource.ListQuery) ([]resource.Row, int, error) {
		s, err := store.GetHuntSettings(ctx)
		if err != nil {
			return nil, 0, nil // no row yet → empty list, SingleRow redirect shows empty form
		}
		row := resource.Row{
			ID: "1", // single-row table always has id=1
			Cells: []resource.Cell{
				{Value: boolStr(s.Enabled)},
				{Value: s.Queries},
				{Value: s.Interval.String()},
			},
			Href: "/admin/hunt_settings/1/edit",
		}
		return []resource.Row{row}, 1, nil
	}
}

// huntSettingsToMap converts HuntSettings to the map[string]string the
// resource.Writer form expects. Bool fields render as "on"/"" for checkboxes.
func huntSettingsToMap(s hunt.HuntSettings) map[string]string {
	return map[string]string{
		"enabled":                 boolStr(s.Enabled),
		"interval":                formatDuration(s.Interval),
		"queries":                 s.Queries,
		"notify_chat_id":          strconv.FormatInt(s.NotifyChatID, 10),
		"notify_min_fit":          strconv.Itoa(s.NotifyMinFit),
		"notify_max_age":          formatDuration(s.NotifyMaxAge),
		"score_enabled":           boolStr(s.ScoreEnabled),
		"score_fail_open":         boolStr(s.ScoreFailOpen),
		"score_min_jaccard":       strconv.Itoa(s.ScoreMinJaccard),
		"score_max_llm_per_cycle": strconv.Itoa(s.ScoreMaxLLMPerCycle),
		"score_sweep_limit":       strconv.Itoa(s.ScoreSweepLimit),
	}
}

// huntSettingsFromForm parses the form map back into HuntSettings.
// Checkbox fields are "on" when checked, absent when unchecked.
func huntSettingsFromForm(v map[string]string) hunt.HuntSettings {
	return hunt.HuntSettings{
		Enabled:             v["enabled"] == "on",
		Interval:            parseDurationDefault(v["interval"], 6*time.Hour),
		Queries:             strings.TrimSpace(v["queries"]),
		NotifyChatID:        parseInt64(v["notify_chat_id"]),
		NotifyMinFit:        clampInt(parseIntDefault(v["notify_min_fit"], 0), 0, 100),
		NotifyMaxAge:        parseDurationDefault(v["notify_max_age"], 48*time.Hour),
		ScoreEnabled:        v["score_enabled"] == "on",
		ScoreFailOpen:       v["score_fail_open"] == "on",
		ScoreMinJaccard:     parseIntDefault(v["score_min_jaccard"], 8),
		ScoreMaxLLMPerCycle: parseIntDefault(v["score_max_llm_per_cycle"], 50),
		ScoreSweepLimit:     parseIntDefault(v["score_sweep_limit"], 50),
	}
}

// formatDuration renders a time.Duration as a short Go-style string.
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

// clampInt clamps v to [lo, hi].
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// parseDurationDefault parses a Go duration string, returning def on error/empty.
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
