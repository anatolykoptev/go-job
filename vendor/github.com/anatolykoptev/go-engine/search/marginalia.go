//nolint:goconst
package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/anatolykoptev/go-engine/metrics"
	"github.com/anatolykoptev/go-engine/sources"
	"github.com/anatolykoptev/go-engine/websearch"
)

const (
	metricMarginaliaRequests = "marginalia_requests"
	marginaliaDirectScore    = 1.0

	// defaultMarginaliaDailyBudget is the default per-calendar-day query cap
	// enforced by MarginaliaBudget when NewMarginaliaBudget is called with a
	// non-positive limit. The Marginalia maintainer granted a personal
	// non-commercial key on an explicit expectation of 50–100 queries/day; 80
	// sits in the middle of that range, leaving headroom on both ends. The
	// fleet's other direct sources run ~1400 dispatches/day each, so wiring
	// Marginalia in without a budget would exceed the granted quota by >10×.
	defaultMarginaliaDailyBudget = 80

	// metricMarginaliaBudgetRemaining is the gauge exposing the remaining
	// Marginalia queries for the current UTC calendar day. The registry
	// namespace supplies the prefix at exposition time so it groups with
	// the sibling fan-out metrics in go-search dashboards. Exhaustion is observable as
	// this gauge reaching 0, rather than inferred from an absence of results.
	metricMarginaliaBudgetRemaining = "marginalia_budget_remaining"

	// marginaliaSourceURL is the link-back the operator promised the
	// Marginalia maintainer in exchange for the personal key. Surfaced in
	// every result's Metadata so a consumer can credit + link the source.
	marginaliaSourceURL = "https://search.marginalia.nu/"
)

// ErrMarginaliaQuotaExhausted is returned by runMarginalia when the daily
// Marginalia budget is spent. It is a deliberate shed outcome, NOT an engine
// failure: callers can errors.Is it to distinguish "out of courtesy quota"
// from a genuine transport/parse error. It is returned promptly (non-blocking)
// and the outbound HTTP request is never issued.
var ErrMarginaliaQuotaExhausted = errors.New("marginalia: daily quota exhausted")

// marginaliaResp is the decoded shape of the Marginalia Nu public search API
// response. The top-level License field (CC-BY-NC-SA 4.0) is surfaced into
// each result's Metadata so consumers can credit + link the source as the
// maintainer's usage terms require.
type marginaliaResp struct {
	License string `json:"license"`
	Results []struct {
		URL         string  `json:"url"`
		Title       string  `json:"title"`
		Description string  `json:"description"`
		Quality     float64 `json:"quality"`
	} `json:"results"`
}

// searchMarginaliaDirect queries the Marginalia Nu search API.
//
// Unexported deliberately: this function issues the HTTP call with no budget
// guard — the per-call quota gate lives in runMarginalia (the sole caller).
// Keeping the HTTP caller package-private is defense-in-depth: no external
// code can reach the transport layer without going through runMarginalia's
// budget.Acquire check, so the quota cannot be spent unguarded. The
// structural invariant (HTTP caller is unexported) is asserted by
// TestMarginalia_HTTPCallerNotExported.
//
// key is the API key path segment (the maintainer grants a personal
// non-commercial key); an empty or whitespace-only key defaults to "public",
// the shared heavily-rate-limited demo key, preserving prior behaviour. The
// key is url.PathEscape'd exactly like the query so a key with an unexpected
// character cannot produce a malformed URL or silently hit a different path.
// Returns nil, nil on 429 (rate-limited) so callers degrade gracefully.
//
// The key is never logged and never included in a returned error: the key is
// redacted from any error string unconditionally (regardless of the error's
// concrete type), not only from *url.Error — a custom BrowserDoer may return
// an error whose text embeds the request URL, and the key is a path segment.
func searchMarginaliaDirect(ctx context.Context, bc BrowserDoer, query, key string, m *metrics.Registry) ([]sources.Result, error) {
	if m != nil {
		m.Incr(metricMarginaliaRequests)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Trim before the empty check so a whitespace-only key falls back to
	// "public" instead of escaping to "%20" and producing a 404.
	key = strings.TrimSpace(key)
	if key == "" {
		key = "public"
	}
	apiURL := "https://api.marginalia.nu/" + url.PathEscape(key) + "/search/" + url.PathEscape(query) + "?count=10"

	// redactKey is the sensitive key value to scrub from error strings. "public"
	// is not sensitive. Both the raw and the url.PathEscape'd form appear in
	// the URL path, so both must be redacted.
	redactKey := key
	if redactKey == "public" {
		redactKey = ""
	}

	headers := websearch.ChromeHeadersFor(bc)
	headers["accept"] = "application/json"

	data, _, status, err := bc.Do(http.MethodGet, apiURL, headers, nil)
	if err != nil {
		// Redact the key from the error string unconditionally, regardless of
		// the error's concrete type. The key is a path segment, so any error
		// text carrying the request URL carries the key. The prior code only
		// stripped *url.Error; a custom BrowserDoer returning a plain error
		// whose text embeds the URL would leak the key through handleSourceError
		// logging. The underlying error chain is preserved (via Unwrap) so
		// errors.Is(err, context.Canceled/DeadlineExceeded) and errors.As for
		// *ErrRateLimited still hold for upstream classification.
		return nil, &redactedError{
			err: err,
			msg: "marginalia: request failed: " + redactKeyFromString(err.Error(), redactKey),
		}
	}
	if status == http.StatusTooManyRequests {
		slog.Warn("marginalia: rate limited (429), skipping")
		return nil, nil
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("marginalia: unexpected status %d", status)
	}

	return ParseMarginaliaJSON(data)
}

// redactedError wraps an error with a sanitized message while preserving the
// underlying error chain via Unwrap. This keeps errors.Is / errors.As working
// (so handleSourceError can still classify context.Canceled, *ErrRateLimited,
// etc.) while guaranteeing the key never appears in the error string.
type redactedError struct {
	err error
	msg string
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.err }

// redactKeyFromString replaces both the raw and the url.PathEscape'd form of
// key with "[redacted]" in s. If key is empty, s is returned unchanged.
func redactKeyFromString(s, key string) string {
	if key == "" {
		return s
	}
	s = strings.ReplaceAll(s, key, "[redacted]")
	if escaped := url.PathEscape(key); escaped != key {
		s = strings.ReplaceAll(s, escaped, "[redacted]")
	}
	return s
}

// ParseMarginaliaJSON decodes Marginalia public API JSON into sources.Result slice.
// Exported for unit tests.
func ParseMarginaliaJSON(data []byte) ([]sources.Result, error) {
	var resp marginaliaResp
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("marginalia: json decode: %w", err)
	}

	results := make([]sources.Result, 0, len(resp.Results))
	for _, item := range resp.Results {
		if item.URL == "" || item.Title == "" {
			continue
		}
		md := map[string]string{
			"engine":     "marginalia",
			"source_url": marginaliaSourceURL,
			"license":    resp.License,
		}
		results = append(results, sources.Result{
			Title:    item.Title,
			URL:      item.URL,
			Content:  item.Description,
			Score:    marginaliaDirectScore,
			Metadata: md,
		})
	}
	return results, nil
}

// MarginaliaBudgetGate is the per-call quota gate consulted by runMarginalia
// before the Marginalia HTTP request is issued. Acquire returns true if the
// call is permitted (and records the spend), or false if the budget is
// exhausted or unavailable.
//
// Fail-closed contract (mirrors the Brave API Redis cap in the consuming repo):
// when the gate cannot count — e.g. the backing store (Redis) is down, the
// client is nil, or the counter is unreadable — Acquire MUST return false. An
// outage must never amplify spend. Do not invert this to fail-open.
//
// The default in-memory implementation is *MarginaliaBudget (resets on process
// restart, limit = defaultMarginaliaDailyBudget). A restart-surviving
// implementation (Redis-backed, UTC calendar day) is injected by the consuming
// repo at composition time — see the report for exactly what the consuming repo
// must provide. When nothing is injected, the package-level default
// (defaultMarginaliaBudget) is used so the courtesy quota is still protected.
type MarginaliaBudgetGate interface {
	Acquire() bool
}

// MarginaliaBudget is a self-imposed per-calendar-day query cap for the
// Marginalia source. The maintainer granted a personal non-commercial key on
// an expectation of 50–100 queries/day; the fleet's other direct sources run
// ~1400 dispatches/day each, so without a cap Marginalia would blow past the
// granted quota by >10× and the key would be withdrawn.
//
// Window: per UTC calendar day (reset at 00:00 UTC). UTC is chosen over a
// host-local timezone because the search service runs on a server where UTC
// is deterministic and host-independent, and a calendar-day boundary (vs a
// rolling 24h window) prevents a burst at a local-TZ midnight from consuming
// 2× the budget across the boundary.
//
// Restart behaviour: the in-memory counter resets to the full limit on every
// process restart. Worst-case daily spend = limit × (restarts+1). With the
// default limit of 80 and a bad day of N restarts, that is 80×(N+1) queries —
// still far below the other sources' ~1400/day but above the 100 promised.
// This repo does NOT carry a Redis dependency for one source; instead the
// budget is exposed as the MarginaliaBudgetGate interface so the consuming repo
// can inject a restart-surviving Redis-backed gate at composition time (same
// shape as internal/engine/brave_api_cap.go). When a persistent gate is
// injected, the default of 80 is the per-calendar-day cap that survives
// restarts — no need to lower it. When nothing is injected, the in-memory
// default applies and the operator controls deploy frequency.
//
// Acquire is non-blocking: when the budget is exhausted it returns false
// promptly so the source sheds load rather than queueing. The remaining budget
// is published to the metricMarginaliaBudgetRemaining gauge so exhaustion is
// observable (gauge → 0) rather than inferred from an absence of results.
//
// A nil *MarginaliaBudget in DirectConfig falls back to a package-level
// default instance (limit = defaultMarginaliaDailyBudget, no metrics registry)
// so the quota is enforced even when the consumer has not wired one; wiring
// NewMarginaliaBudget with the consumer's *metrics.Registry additionally
// publishes the remaining-budget gauge.
type MarginaliaBudget struct {
	limit int
	now   func() time.Time
	m     *metrics.Registry

	mu    sync.Mutex
	day   string // "2006-01-02" UTC of the current window
	spent int    // calls issued in the current window
}

// NewMarginaliaBudget returns a MarginaliaBudget with the given per-day limit.
// A non-positive limit defaults to defaultMarginaliaDailyBudget. When m is
// non-nil the remaining-budget gauge is published.
func NewMarginaliaBudget(limit int, m *metrics.Registry) *MarginaliaBudget {
	return newMarginaliaBudget(limit, m, time.Now)
}

// newMarginaliaBudget is the testable constructor with an injectable clock.
func newMarginaliaBudget(limit int, m *metrics.Registry, now func() time.Time) *MarginaliaBudget {
	if limit <= 0 {
		limit = defaultMarginaliaDailyBudget
	}
	b := &MarginaliaBudget{limit: limit, now: now, m: m}
	b.reset(now())
	return b
}

// reset starts a fresh window for the given time's UTC calendar day.
func (b *MarginaliaBudget) reset(t time.Time) {
	b.day = t.UTC().Format("2006-01-02")
	b.spent = 0
	b.publishRemaining()
}

// Acquire records one query against the daily budget. It returns true if the
// query is permitted (and decrements remaining), or false if the budget is
// exhausted — non-blocking, never waits. On UTC calendar-day rollover the
// counter resets to the full limit before evaluating the call.
func (b *MarginaliaBudget) Acquire() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	today := b.now().UTC().Format("2006-01-02")
	if today != b.day {
		b.day = today
		b.spent = 0
	}
	if b.spent >= b.limit {
		b.publishRemaining()
		return false
	}
	b.spent++
	b.publishRemaining()
	return true
}

// publishRemaining sets the gauge to limit-spent. Called under b.mu.
func (b *MarginaliaBudget) publishRemaining() {
	if b.m == nil {
		return
	}
	b.m.Gauge(metricMarginaliaBudgetRemaining).Set(float64(b.limit - b.spent))
}

// defaultMarginaliaBudget is the package-level fallback used when a
// DirectConfig enables Marginalia without wiring a *MarginaliaBudget. It
// enforces the default limit so the courtesy quota is protected even under
// operator misconfiguration, but it has no metrics registry and so does not
// publish the remaining-budget gauge (wire NewMarginaliaBudget with the
// consumer's registry for observability).
var defaultMarginaliaBudget = newMarginaliaBudget(defaultMarginaliaDailyBudget, nil, time.Now)
