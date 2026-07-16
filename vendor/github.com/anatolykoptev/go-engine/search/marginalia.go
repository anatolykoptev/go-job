//nolint:goconst
package search

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/anatolykoptev/go-engine/metrics"
	"github.com/anatolykoptev/go-engine/sources"
	"github.com/anatolykoptev/go-engine/websearch"
)

const (
	metricMarginaliaRequests = "marginalia_requests"
	marginaliaDirectScore    = 1.0
)

// marginaliaResp is the decoded shape of the Marginalia public API response.
type marginaliaResp struct {
	Results []struct {
		URL         string  `json:"url"`
		Title       string  `json:"title"`
		Description string  `json:"description"`
		Quality     float64 `json:"quality"`
	} `json:"results"`
}

// SearchMarginaliaDirect queries the Marginalia Nu public search API.
// Returns nil, nil on 429 (rate-limited) so callers degrade gracefully.
func SearchMarginaliaDirect(ctx context.Context, bc BrowserDoer, query string, m *metrics.Registry) ([]sources.Result, error) {
	if m != nil {
		m.Incr(metricMarginaliaRequests)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	apiURL := "https://api.marginalia.nu/public/search/" + url.PathEscape(query) + "?count=10"

	headers := websearch.ChromeHeaders()
	headers["accept"] = "application/json"

	data, _, status, err := bc.Do(http.MethodGet, apiURL, headers, nil)
	if err != nil {
		return nil, err
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
		results = append(results, sources.Result{
			Title:    item.Title,
			URL:      item.URL,
			Content:  item.Description,
			Score:    marginaliaDirectScore,
			Metadata: map[string]string{"engine": "marginalia"},
		})
	}
	return results, nil
}
