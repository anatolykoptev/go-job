package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/anatolykoptev/go_job/internal/engine"
)

const (
	himalayasAPIURL   = "https://himalayas.app/jobs/api"
	himalayasCacheKey = "himalayas_jobs"
)

type himalayasResponse struct {
	Jobs  []himalayasJob `json:"jobs"`
	Total int            `json:"totalCount"`
}

type himalayasJob struct {
	Title          string   `json:"title"`
	CompanyName    string   `json:"companyName"`
	ApplicationURL string   `json:"applicationLink"`
	Categories     []string `json:"categories"`
	Seniority      []string `json:"seniority"`
	// MinSalary/MaxSalary are nullable and may be fractional (himalayas returns
	// null for undisclosed salaries and a fractional number for hourly rates,
	// e.g. 26.5). Declaring int made encoding/json abort the WHOLE Unmarshal on
	// the first fractional value, losing every himalayas job on every cycle.
	// *float64 accepts null (→ nil) and any number; annualizeHimalayasSalary
	// converts to the annual int engine.FreelanceJob.SalaryMin/Max expects.
	MinSalary    *float64        `json:"minSalary"`
	MaxSalary    *float64        `json:"maxSalary"`
	SalaryPeriod string          `json:"salaryPeriod"`
	PubDate      json.RawMessage `json:"pubDate"`
	Excerpt      string          `json:"excerpt"`
}

// SearchHimalayas fetches jobs from Himalayas. Results are cached.
func SearchHimalayas(ctx context.Context, query string, limit int) ([]engine.FreelanceJob, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	cacheKey := himalayasCacheKey
	if query != "" {
		cacheKey = himalayasCacheKey + "_" + query
	}

	if cached, ok := engine.CacheLoadJSON[[]engine.FreelanceJob](ctx, cacheKey); ok {
		slog.Debug("himalayas: using cached results", slog.Int("results", len(cached)))
		if len(cached) > limit {
			cached = cached[:limit]
		}
		return cached, nil
	}

	jobs, err := fetchHimalayas(ctx, query, limit)
	if err != nil {
		return nil, err
	}

	engine.CacheStoreJSON(ctx, cacheKey, "", jobs)
	if len(jobs) > limit {
		jobs = jobs[:limit]
	}

	slog.Debug("himalayas: fetch complete", slog.Int("results", len(jobs)))
	return jobs, nil
}

func fetchHimalayas(ctx context.Context, query string, limit int) ([]engine.FreelanceJob, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, engine.Cfg.FetchTimeout)
	defer cancel()

	params := url.Values{}
	if query != "" {
		params.Set("q", query)
	}
	params.Set("limit", strconv.Itoa(limit))

	apiURL := himalayasAPIURL + "?" + params.Encode()

	// Route through the hardened proxy fetcher (DirectFirst Chrome-TLS -> Webshare
	// proxy pool -> oxbrowser/byparr CF-solver). himalayas.app sits behind Cloudflare
	// which fingerprints the TLS ClientHello (JA3); the stdlib http.Client has a
	// bot-signature JA3 even with a spoofed Chrome User-Agent, reliably returning 403.
	body, err := engine.FetchProxyBody(fetchCtx, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("himalayas fetch failed: %w", err)
	}

	return parseHimalayasResponse(body)
}

func parseHimalayasResponse(data []byte) ([]engine.FreelanceJob, error) {
	var resp himalayasResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("himalayas: JSON parse failed: %w", err)
	}

	if len(resp.Jobs) == 0 {
		return nil, nil
	}

	jobs := make([]engine.FreelanceJob, 0, len(resp.Jobs))
	for _, hj := range resp.Jobs {
		if hj.ApplicationURL == "" {
			continue
		}
		tags := hj.Categories
		if len(hj.Seniority) > 0 {
			tags = append(tags, hj.Seniority...)
		}
		jobs = append(jobs, engine.FreelanceJob{
			Title:     hj.Title,
			Company:   hj.CompanyName,
			URL:       hj.ApplicationURL,
			Tags:      tags,
			SalaryMin: annualizeHimalayasSalary(hj.MinSalary, hj.SalaryPeriod),
			SalaryMax: annualizeHimalayasSalary(hj.MaxSalary, hj.SalaryPeriod),
			Source:    "himalayas",
			Posted:    parsePubDate(hj.PubDate),
		})
	}

	return jobs, nil
}

// annualizeHimalayasSalary converts a nullable fractional himalayas salary to
// the int annual figure engine.FreelanceJob.SalaryMin/Max expects. Returns 0
// for nil (undisclosed).
//
// salaryPeriod is honoured (case-insensitively): "hourly" rates are normalised
// to annual using the conventional 2080-hour US full-time equivalent (40 h/wk ×
// 52 wk) so hourly and annual listings are comparable in the same field.
// "annual" and "yearly" (a plausible API synonym) plus the empty string (for
// back-compat with sources that omit the field) pass through as-is. Any other
// period (daily, weekly, monthly, …) returns 0 — we do not invent a conversion
// factor for unknown periods, and a slog.Warn names the period so a silent
// salary-NULL at scale is observable.
//
// 0 means "undisclosed" end to end on the freelance path: nullInt at
// hunt/store.go:664 writes NULL for BudgetMin/BudgetMax, and the freelance
// Telegram card at hunt/notify/telegram.go:419 omits the "Budget: $N" line
// when BudgetMax == 0 (gating on BudgetMax alone — a min-only row also
// renders no budget line, a pre-existing asymmetry out of scope here). The
// row is still stored, upserted and notified; only the budget figure is
// absent. This is NOT the hunt.Job path (nullInt at hunt/store.go:424,
// scorer.go:370, telegram.go:352) — himalayas rows never enter it.
func annualizeHimalayasSalary(v *float64, period string) int {
	if v == nil {
		return 0
	}
	switch strings.ToLower(period) {
	case "hourly":
		return int(math.Round(*v * 2080))
	case "annual", "yearly", "":
		return int(math.Round(*v))
	default:
		// daily, weekly, monthly, … — fail closed (0 = undisclosed) rather
		// than pass an unknown period through as annual. Log the period so a
		// silent salary-NULL at scale is observable, not swallowed.
		slog.Warn("himalayas: unrecognised salaryPeriod, failing closed to 0",
			slog.String("period", period))
		return 0
	}
}

// parsePubDate handles pubDate as either a JSON string or a Unix timestamp number.
func parsePubDate(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	s := strings.TrimSpace(string(raw))
	if len(s) > 1 && s[0] == '"' {
		var str string
		if json.Unmarshal(raw, &str) == nil {
			return str
		}
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if n > 1e12 {
			n /= 1000
		}
		return time.Unix(n, 0).UTC().Format(time.RFC3339)
	}
	return s
}
