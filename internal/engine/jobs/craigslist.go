package jobs

import (
	"context"
	"log/slog"
	"strings"

	"github.com/anatolykoptev/go_job/internal/engine"
)

const craigslistSiteSearch = "site:craigslist.org/d/jobs"

// SearchCraigslistJobs searches Craigslist job listings via SearXNG site: query.
func SearchCraigslistJobs(ctx context.Context, query, location string, limit int) ([]engine.SearxngResult, error) {
	engine.IncrCraigslistRequests()

	searxQuery := query + " " + craigslistSiteSearch
	if location != "" {
		searxQuery = query + " " + location + " " + craigslistSiteSearch
	}

	searxResults, err := engine.SearchSearXNG(ctx, searxQuery, "en", "", "google")
	if err != nil {
		slog.Warn("craigslist: SearXNG error", slog.Any("error", err))
	}

	var results []engine.SearxngResult
	for _, r := range searxResults {
		if strings.Contains(r.URL, "craigslist.org") {
			r.Content = "**Source:** Craigslist\n\n" + r.Content
			r.Score = 0.7
			results = append(results, r)
		}
	}

	if len(results) > limit {
		results = results[:limit]
	}

	slog.Debug("craigslist: search complete", slog.Int("results", len(results)))
	return results, nil
}
