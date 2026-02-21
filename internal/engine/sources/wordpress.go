package sources

import (
	"github.com/anatolykoptev/go_job/internal/engine"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// WordPress Code Reference post types to search.
var wpPostTypes = []struct{ PostType, Label string }{
	{"wp-parser-function", "function"},
	{"wp-parser-hook", "hook"},
	{"wp-parser-class", "class"},
	{"wp-parser-method", "method"},
}

// wpAPIItem represents a single item from the WordPress REST API.
type wpAPIItem struct {
	Title   struct{ Rendered string } `json:"title"`
	Link    string                    `json:"link"`
	Excerpt struct{ Rendered string } `json:"excerpt"`
}

// SearchWordPressAPI queries the WordPress Code Reference REST API for all 4 post types in parallel.
func SearchWordPressAPI(ctx context.Context, query string) ([]engine.SearxngResult, error) {
	type result struct {
		items []engine.SearxngResult
		err   error
	}

	ch := make(chan result, len(wpPostTypes))
	var wg sync.WaitGroup

	for _, pt := range wpPostTypes {
		wg.Add(1)
		go func(postType, label string) {
			defer wg.Done()
			items, err := fetchWPPostType(ctx, query, postType, label)
			ch <- result{items, err}
		}(pt.PostType, pt.Label)
	}

	go func() {
		wg.Wait()
		close(ch)
	}()

	var all []engine.SearxngResult
	for res := range ch {
		if res.err != nil {
			slog.Warn("wordpress API error", slog.Any("error", res.err))
			continue
		}
		all = append(all, res.items...)
	}
	return all, nil
}

// fetchWPPostType fetches results for a single WordPress post type.
func fetchWPPostType(ctx context.Context, query, postType, label string) ([]engine.SearxngResult, error) {
	ctx, cancel := context.WithTimeout(ctx, engine.Cfg.FetchTimeout)
	defer cancel()

	apiURL := fmt.Sprintf("https://developer.wordpress.org/wp-json/wp/v2/%s?%s",
		postType,
		url.Values{"search": {query}, "per_page": {"3"}}.Encode(),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", engine.UserAgentBot)

	resp, err := engine.RetryHTTP(ctx, engine.DefaultRetryConfig, func() (*http.Response, error) {
		return engine.Cfg.HTTPClient.Do(req)
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("wordpress API status %d for %s", resp.StatusCode, postType)
	}

	var items []wpAPIItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, err
	}

	var results []engine.SearxngResult
	for _, item := range items {
		title := strings.TrimSpace(item.Title.Rendered)
		if title == "" || item.Link == "" {
			continue
		}

		// Build content: clean excerpt + type label
		var parts []string
		if excerpt := engine.CleanHTML(item.Excerpt.Rendered); excerpt != "" {
			parts = append(parts, excerpt)
		}
		parts = append(parts, "Type: "+label)
		content := strings.Join(parts, " | ")
		if len(content) > 500 {
			content = content[:497] + "..."
		}

		results = append(results, engine.SearxngResult{
			Title:   title,
			URL:     item.Link,
			Content: content,
		})
	}
	return results, nil
}
