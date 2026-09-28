package adminui

// huntsettings.go — per-account hunt worker settings, exposed as a go-panel
// resource.Resource with SingleRow=true.
//
// P3 (ADR-7): the resource is ACCOUNT-SCOPED. The row key is the acting
// account's UUID (resolved through the same accountResolver seam every other
// admin handler uses), reads and writes go through the account-bound facade
// (*hunt.AccountStore), and a missing account_hunt_settings row renders as
// the disabled/default form — one account can never read or write another
// account's row through this resource.
//
// Fleet-global knobs are gone from the form deliberately: cycle interval and
// the sweep limit are env/deploy config now (HUNT_INGEST_INTERVAL,
// HUNT_SCORE_SWEEP_LIMIT), and the per-account score_max_llm_per_cycle is a
// SUB-CAP under the fleet cap (empty = fleet cap only).

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/anatolykoptev/go-kit/admintable"
	"github.com/anatolykoptev/go-panel/resource"
	"github.com/anatolykoptev/go-panel/tenant"
	"github.com/anatolykoptev/go_job/internal/accounts"
	"github.com/anatolykoptev/go_job/internal/hunt"
)

// errNoAccount denies a settings read/write whose request carries no verified
// account identity — fail-closed, never falls back to a shared row.
var errNoAccount = errors.New("no account identity in request context")

// huntSettingsResourceSpec is the admintable spec for the single-row list view.
// SingleRow resources still need a Lister (to find the row ID for the redirect).
var huntSettingsResourceSpec = admintable.Spec{
	Columns: []admintable.Column{
		{Key: "account", Label: "Account", Sortable: false, SQLExpr: "account", Width: "9rem"},
		{Key: "enabled", Label: "Enabled", Sortable: true, SQLExpr: "enabled::text", Width: "6rem"},
		{Key: "queries", Label: "Queries", Sortable: false, SQLExpr: "queries"},
	},
	DefaultKey: "enabled",
	DefaultDir: admintable.Asc,
}

// huntSettingsResource returns a go-panel resource.Resource bound to the
// ACTING ACCOUNT's account_hunt_settings row. Registered via resource.Register
// in adminui.go.
func huntSettingsResource(store *hunt.Store, acctOf accountResolver) resource.Resource {
	return resource.Resource{
		Name:      "hunt_settings",
		Title:     "Hunt Settings",
		Icon:      "⚙",
		Group:     grpHunt,
		Sort:      huntSettingsResourceSpec,
		Filter:    admintable.FilterSpec{},
		SingleRow: true,
		Lister:    huntSettingsLister(store, acctOf),
		FetchRow: func(ctx context.Context, _ string) (map[string]string, error) {
			aid, ok := acctOf(ctx)
			if !ok {
				return nil, errNoAccount
			}
			s, _, err := store.ForAccount(aid).GetHuntSettings(ctx)
			if err != nil {
				return nil, err
			}
			return huntSettingsToMap(s), nil
		},
		Writer: &resource.Writer{
			Form: resource.FormSpec{Fields: []resource.Field{
				{Key: "enabled", Label: "Ingest Enabled", Kind: resource.FieldCheckbox, Help: "Run the hunt worker for this account"},
				{Key: "queries", Label: "Search Queries (comma-separated)", Kind: resource.FieldText, Help: "Generic role/skill strings. No company names (public repo). Applied per-cycle."},
				{Key: "notify_chat_id", Label: "Telegram Chat ID", Kind: resource.FieldText, Help: "0 = no Telegram notify (uses env fallback)."},
				{Key: "notify_min_fit", Label: "Min Fit Score (0-100)", Kind: resource.FieldNumber, Help: "Only notify jobs with fit_score ≥ this. 0 = gate open. Applied per-cycle."},
				{Key: "notify_max_age", Label: "Max Job Age (Go duration)", Kind: resource.FieldText, Help: "Recency gate — jobs posted older than this → stale (no LLM). e.g. 48h. Applied per-cycle."},
				{Key: "score_enabled", Label: "Scoring Enabled", Kind: resource.FieldCheckbox, Help: "Use LLM to score job fit for this account. Applied per-cycle."},
				{Key: "score_fail_open", Label: "Fail-Open on LLM Error", Kind: resource.FieldCheckbox, Help: "Notify with degraded card on LLM error (unchecked = drop). Default false (#167)."},
				{Key: "score_min_jaccard", Label: "Min Jaccard (0-100)", Kind: resource.FieldNumber, Help: "Pre-filter threshold; below → reject without LLM. Applied per-cycle."},
				{Key: "score_max_llm_per_cycle", Label: "Max LLM Calls per Cycle", Kind: resource.FieldNumber, Help: "Per-account LLM sub-cap under the fleet cap. Empty = fleet cap only."},
			}},
			Load: func(ctx context.Context, _ tenant.Tenant, _ string) (map[string]string, error) {
				aid, ok := acctOf(ctx)
				if !ok {
					return nil, errNoAccount
				}
				s, _, err := store.ForAccount(aid).GetHuntSettings(ctx)
				if err != nil {
					return nil, err
				}
				return huntSettingsToMap(s), nil
			},
			Save: func(ctx context.Context, _ tenant.Tenant, _ string, v map[string]string) error {
				aid, ok := acctOf(ctx)
				if !ok {
					return errNoAccount
				}
				s := huntSettingsFromForm(v)
				if err := store.ForAccount(aid).SaveHuntSettings(ctx, s); err != nil {
					return resource.NewSaveError("queries", err.Error())
				}
				return nil
			},
			RedirectAfterSave: func(_ context.Context, _ string) string { return "/admin/hunt_settings" },
		},
	}
}

// huntSettingsLister returns a single-row lister keyed by the ACTING account's
// UUID — the row the SingleRow redirect opens is this account's own settings.
// A request without a verified account returns an empty list (the SingleRow
// form then renders disabled defaults, never another account's row).
func huntSettingsLister(store *hunt.Store, acctOf accountResolver) func(context.Context, resource.ListQuery) ([]resource.Row, int, error) {
	return func(ctx context.Context, _ resource.ListQuery) ([]resource.Row, int, error) {
		aid, ok := acctOf(ctx)
		if !ok {
			return []resource.Row{}, 0, nil
		}
		s, _, err := store.ForAccount(aid).GetHuntSettings(ctx)
		if err != nil {
			return nil, 0, err
		}
		row := resource.Row{
			ID: aid.String(),
			Cells: []resource.Cell{
				{Value: aid.String()},
				{Value: boolStr(s.Enabled)},
				{Value: s.Queries},
			},
			Href: "/admin/hunt_settings/" + aid.String() + "/edit",
		}
		return []resource.Row{row}, 1, nil
	}
}

// huntSettingsToMap converts accounts.AccountHuntSettings to the
// map[string]string the resource.Writer form expects. Bool fields render as
// "on"/"" for checkboxes; a nil ScoreMaxLLMPerCycle renders "" (fleet cap only).
func huntSettingsToMap(s accounts.AccountHuntSettings) map[string]string {
	maxLLM := ""
	if s.ScoreMaxLLMPerCycle != nil {
		maxLLM = strconv.Itoa(*s.ScoreMaxLLMPerCycle)
	}
	return map[string]string{
		"enabled":                 boolStr(s.Enabled),
		"queries":                 s.Queries,
		"notify_chat_id":          strconv.FormatInt(s.NotifyChatID, 10),
		"notify_min_fit":          strconv.Itoa(s.NotifyMinFit),
		"notify_max_age":          formatDuration(s.NotifyMaxAge),
		"score_enabled":           boolStr(s.ScoreEnabled),
		"score_fail_open":         boolStr(s.ScoreFailOpen),
		"score_min_jaccard":       strconv.Itoa(s.ScoreMinJaccard),
		"score_max_llm_per_cycle": maxLLM,
	}
}

// huntSettingsFromForm parses the form map back into
// accounts.AccountHuntSettings. Checkbox fields are "on" when checked, absent
// when unchecked; an empty score_max_llm_per_cycle maps to nil (no sub-cap).
func huntSettingsFromForm(v map[string]string) accounts.AccountHuntSettings {
	var maxLLM *int
	if raw := strings.TrimSpace(v["score_max_llm_per_cycle"]); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			maxLLM = &n
		}
	}
	return accounts.AccountHuntSettings{
		Enabled:             v["enabled"] == "on",
		Queries:             strings.TrimSpace(v["queries"]),
		NotifyChatID:        parseInt64(v["notify_chat_id"]),
		NotifyMinFit:        clampInt(parseIntDefault(v["notify_min_fit"], 0), 0, 100),
		NotifyMaxAge:        parseDurationDefault(v["notify_max_age"], 48*time.Hour),
		ScoreEnabled:        v["score_enabled"] == "on",
		ScoreFailOpen:       v["score_fail_open"] == "on",
		ScoreMinJaccard:     parseIntDefault(v["score_min_jaccard"], 8),
		ScoreMaxLLMPerCycle: maxLLM,
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
