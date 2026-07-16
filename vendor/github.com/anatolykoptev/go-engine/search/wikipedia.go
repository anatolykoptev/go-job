//nolint:goconst
package search

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/anatolykoptev/go-engine/metrics"
	"github.com/anatolykoptev/go-engine/sources"
	"github.com/anatolykoptev/go-engine/websearch"
)

const (
	metricWikipediaRequests = "wikipedia_requests"
	wikipediaDirectScore    = 1.0
)

var reHTMLTagWiki = regexp.MustCompile(`<[^>]*>`)

// wikipediaSearchResp is the decoded shape of a MediaWiki search API response.
type wikipediaSearchResp struct {
	Query struct {
		Search []struct {
			Title   string `json:"title"`
			Snippet string `json:"snippet"`
			PageID  int    `json:"pageid"`
		} `json:"search"`
	} `json:"query"`
}

// SearchWikipediaDirect queries the Wikipedia MediaWiki search API.
// lang should be an ISO 639-1 code ("en", "ru", …).
// Returns nil, nil on 429 (rate-limited) so callers degrade gracefully.
func SearchWikipediaDirect(ctx context.Context, bc BrowserDoer, query, lang string, m *metrics.Registry) ([]sources.Result, error) {
	if m != nil {
		m.Incr(metricWikipediaRequests)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	apiURL := fmt.Sprintf(
		"https://%s.wikipedia.org/w/api.php?action=query&list=search&srsearch=%s&format=json&srlimit=10",
		lang, url.QueryEscape(query),
	)

	headers := websearch.ChromeHeaders()
	headers["accept"] = "application/json"

	data, _, status, err := bc.Do(http.MethodGet, apiURL, headers, nil)
	if err != nil {
		return nil, err
	}
	if status == http.StatusTooManyRequests {
		slog.Warn("wikipedia: rate limited (429), skipping")
		return nil, nil
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("wikipedia: unexpected status %d", status)
	}

	return ParseWikipediaJSON(data, lang)
}

// ParseWikipediaJSON decodes MediaWiki search API JSON into sources.Result slice.
// Exported for unit tests.
func ParseWikipediaJSON(data []byte, lang string) ([]sources.Result, error) {
	var resp wikipediaSearchResp
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("wikipedia: json decode: %w", err)
	}

	results := make([]sources.Result, 0, len(resp.Query.Search))
	for _, item := range resp.Query.Search {
		if item.Title == "" {
			continue
		}
		u := "https://" + lang + ".wikipedia.org/wiki/" + url.PathEscape(strings.ReplaceAll(item.Title, " ", "_"))
		content := strings.TrimSpace(reHTMLTagWiki.ReplaceAllString(item.Snippet, ""))
		results = append(results, sources.Result{
			Title:    item.Title,
			URL:      u,
			Content:  content,
			Score:    wikipediaDirectScore,
			Metadata: map[string]string{"engine": "wikipedia"},
		})
	}
	return results, nil
}
