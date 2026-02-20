package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/anatolykoptev/go_job/internal/engine"
	"golang.org/x/net/html"
)

const indeedSiteSearch = "site:indeed.com/viewjob"

// SearchIndeedJobs discovers job listings on Indeed via SearXNG site: search,
// then enriches results with JSON-LD structured data from individual job pages.
func SearchIndeedJobs(ctx context.Context, query, location string, limit int) ([]engine.SearxngResult, error) {
	engine.IncrIndeedRequests()

	searxQuery := query + " " + indeedSiteSearch
	if location != "" {
		searxQuery = query + " " + location + " " + indeedSiteSearch
	}

	// Search via both Google and Bing for better coverage.
	type searchRes struct {
		results []engine.SearxngResult
		err     error
	}
	gCh := make(chan searchRes, 1)
	bCh := make(chan searchRes, 1)

	go func() {
		r, err := engine.SearchSearXNG(ctx, searxQuery, "all", "", "google")
		gCh <- searchRes{r, err}
	}()
	go func() {
		r, err := engine.SearchSearXNG(ctx, searxQuery, "all", "", "bing")
		bCh <- searchRes{r, err}
	}()

	gr := <-gCh
	br := <-bCh

	if gr.err != nil {
		slog.Warn("indeed: google SearXNG error", slog.Any("error", gr.err))
	}
	if br.err != nil {
		slog.Warn("indeed: bing SearXNG error", slog.Any("error", br.err))
	}

	// Merge and dedup by URL.
	seen := make(map[string]bool)
	var merged []engine.SearxngResult
	for _, r := range append(gr.results, br.results...) {
		if !seen[r.URL] && strings.Contains(r.URL, "indeed.com/viewjob") {
			seen[r.URL] = true
			merged = append(merged, r)
		}
	}

	if len(merged) == 0 {
		slog.Debug("indeed: no viewjob URLs found")
		return nil, nil
	}

	if len(merged) > limit {
		merged = merged[:limit]
	}

	// Enrich top results with structured data from job pages.
	type enrichResult struct {
		idx     int
		content string
	}
	enrichCh := make(chan enrichResult, len(merged))
	fetchCount := len(merged)
	if fetchCount > 5 {
		fetchCount = 5
	}

	for i := 0; i < fetchCount; i++ {
		go func(idx int, r engine.SearxngResult) {
			if idx > 0 {
				select {
				case <-ctx.Done():
					enrichCh <- enrichResult{idx, ""}
					return
				default:
				}
			}
			content := fetchIndeedJobContent(ctx, r)
			enrichCh <- enrichResult{idx, content}
		}(i, merged[i])
	}

	for i := 0; i < fetchCount; i++ {
		res := <-enrichCh
		if res.content != "" {
			merged[res.idx].Content = res.content
		}
	}

	slog.Debug("indeed: search complete", slog.Int("results", len(merged)))
	return merged, nil
}

// fetchIndeedJobContent fetches an Indeed job page and extracts structured content.
func fetchIndeedJobContent(ctx context.Context, r engine.SearxngResult) string {
	_, bodyText, err := engine.FetchURLContent(ctx, r.URL)
	if err != nil || bodyText == "" {
		// Fall back to SearXNG snippet.
		if r.Content != "" {
			return "**Source:** Indeed\n\n" + r.Content
		}
		return ""
	}

	// Try to parse JSON-LD JobPosting from the page.
	if structured := extractIndeedStructured(bodyText); structured != "" {
		return structured
	}

	// Fall back to snippet from SearXNG.
	content := "**Source:** Indeed"
	if r.Content != "" {
		content += "\n\n" + engine.TruncateRunes(r.Content, 800, "...")
	}
	return content
}

// extractIndeedStructured tries to extract job info from Indeed page HTML/text.
func extractIndeedStructured(body string) string {
	// Indeed embeds JSON-LD with schema.org/JobPosting — reuse LinkedIn's extractor logic.
	if jsonLD := extractJSONLD(body); jsonLD != "" {
		return "**Source:** Indeed\n\n" + jsonLD
	}

	// Try HTML parsing for job title + company + location.
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return ""
	}

	var parts []string

	// Indeed uses data-testid attributes for key fields.
	testIDs := map[string]string{
		"jobsearch-JobInfoHeader-title":        "**Title:**",
		"inlineHeader-companyName":             "**Company:**",
		"jobsearch-JobInfoHeader-companyLocation": "**Location:**",
	}
	for testID, label := range testIDs {
		if n := findByAttr(doc, "data-testid", testID); n != nil {
			text := strings.TrimSpace(textContent(n))
			if text != "" {
				parts = append(parts, label+" "+text)
			}
		}
	}

	if len(parts) == 0 {
		return ""
	}

	return "**Source:** Indeed\n\n" + strings.Join(parts, "\n")
}

// findByAttr finds the first element with a given attribute value.
func findByAttr(n *html.Node, attr, value string) *html.Node {
	if n.Type == html.ElementNode {
		for _, a := range n.Attr {
			if a.Key == attr && strings.Contains(a.Val, value) {
				return n
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findByAttr(c, attr, value); found != nil {
			return found
		}
	}
	return nil
}

// indeedJobURL returns a canonical Indeed job URL from a viewjob URL.
func indeedJobURL(rawURL string) string {
	if strings.Contains(rawURL, "indeed.com/viewjob") {
		return rawURL
	}
	return fmt.Sprintf("https://www.indeed.com/viewjob?jk=%s", rawURL)
}
