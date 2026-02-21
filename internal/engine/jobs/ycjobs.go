package jobs

import (
	"github.com/anatolykoptev/go_job/internal/engine"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

const ycJobsSearchURL = "https://www.workatastartup.com/jobs"
const ycSiteSearch = "site:workatastartup.com"

// SearchYCJobs searches workatastartup.com for YC startup job listings.
// Strategy: SearXNG site: query to find job URLs + optional direct page scrape.
func SearchYCJobs(ctx context.Context, query, location string, limit int) ([]engine.SearxngResult, error) {
	engine.IncrYCJobsRequests()

	// Primary: SearXNG site: search — fast, good coverage.
	searxQuery := query + " " + ycSiteSearch
	if location != "" {
		searxQuery = query + " " + location + " " + ycSiteSearch
	}

	searxResults, err := engine.SearchSearXNG(ctx, searxQuery, "all", "", "google")
	if err != nil {
		slog.Warn("yc: SearXNG error", slog.Any("error", err))
	}

	// Filter to only workatastartup.com URLs.
	var ycResults []engine.SearxngResult
	for _, r := range searxResults {
		if strings.Contains(r.URL, "workatastartup.com") {
			r.Content = "**Source:** YC workatastartup.com\n\n" + r.Content
			r.Score = 0.85
			ycResults = append(ycResults, r)
		}
	}

	// Secondary: direct scrape of search page for richer data.
	if len(ycResults) < limit {
		direct, err := scrapeYCJobsPage(ctx, query, location)
		if err != nil {
			slog.Debug("yc: direct scrape failed", slog.Any("error", err))
		} else {
			// Merge, dedup by URL.
			seen := make(map[string]bool)
			for _, r := range ycResults {
				seen[r.URL] = true
			}
			for _, r := range direct {
				if !seen[r.URL] {
					seen[r.URL] = true
					ycResults = append(ycResults, r)
				}
			}
		}
	}

	if len(ycResults) > limit {
		ycResults = ycResults[:limit]
	}

	slog.Debug("yc: search complete", slog.Int("results", len(ycResults)))
	return ycResults, nil
}

// scrapeYCJobsPage directly fetches workatastartup.com/jobs and parses job cards.
func scrapeYCJobsPage(ctx context.Context, query, location string) ([]engine.SearxngResult, error) {
	u, err := url.Parse(ycJobsSearchURL)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	if query != "" {
		q.Set("q", query)
	}
	if location != "" {
		q.Set("l", location)
	}
	u.RawQuery = q.Encode()
	targetURL := u.String()

	var bodyBytes []byte
	if engine.Cfg.BrowserClient != nil {
		headers := engine.ChromeHeaders()
		headers["referer"] = "https://www.workatastartup.com/"
		data, _, status, err := engine.Cfg.BrowserClient.Do("GET", targetURL, headers, nil)
		if err != nil {
			return nil, fmt.Errorf("yc browser fetch: %w", err)
		}
		if status != 200 {
			return nil, fmt.Errorf("yc status %d", status)
		}
		bodyBytes = data
	} else {
		return nil, errors.New("BrowserClient not available for YC direct scrape")
	}

	return parseYCJobsHTML(string(bodyBytes), targetURL), nil
}

// parseYCJobsHTML extracts job listings from workatastartup.com search results HTML.
func parseYCJobsHTML(body, pageURL string) []engine.SearxngResult {
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return nil
	}

	var results []engine.SearxngResult

	// Find job listing elements — workatastartup uses data attributes and class patterns.
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			cls := getAttr(n, "class")
			// Job cards typically have "job-name" or "directory-list-job" classes.
			if strings.Contains(cls, "job-name") || strings.Contains(cls, "directory-list-job") || strings.Contains(cls, "jobs-list-item") {
				result := extractYCJobCard(n, pageURL)
				if result.Title != "" {
					results = append(results, result)
				}
				return
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	return results
}

// extractYCJobCard extracts a job from a card node.
func extractYCJobCard(n *html.Node, pageURL string) engine.SearxngResult {
	var title, company, location, jobURL string

	// Try to find <a> with href for job URL.
	for _, a := range findElements(n, "a") {
		href := getAttr(a, "href")
		if strings.Contains(href, "/jobs/") || strings.Contains(href, "workatastartup.com") {
			if strings.HasPrefix(href, "/") {
				href = "https://www.workatastartup.com" + href
			}
			jobURL = href
			text := strings.TrimSpace(textContent(a))
			if text != "" && title == "" {
				title = text
			}
		}
	}

	// Extract company and location from text nodes.
	allText := strings.TrimSpace(textContent(n))
	lines := strings.Split(allText, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || line == title {
			continue
		}
		switch {
		case company == "":
			company = line
		case location == "":
			location = line
		default:
			// all fields filled
		}
	}

	if title == "" && company != "" {
		title = company
		company = ""
	}

	if jobURL == "" {
		jobURL = pageURL
	}

	content := "**Source:** YC workatastartup.com"
	if company != "" {
		content += " | **Company:** " + company
	}
	if location != "" {
		content += " | **Location:** " + location
	}

	return engine.SearxngResult{
		Title:   title,
		Content: content,
		URL:     jobURL,
		Score:   0.85,
	}
}
