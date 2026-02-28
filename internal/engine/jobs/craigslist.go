package jobs

import (
	"context"
	"log/slog"
	"regexp"
	"strings"

	"github.com/anatolykoptev/go_job/internal/engine"
)

const craigslistSiteSearch = "site:craigslist.org"

// craigslistListingRe matches individual Craigslist job posting URLs
// (e.g. sfbay.craigslist.org/pen/sof/d/san-mateo-senior-engineer/7856959859.html).
var craigslistListingRe = regexp.MustCompile(`craigslist\.org/.+/\d+\.html`)

// craigslistJobCategories are the Craigslist sections that contain job postings.
var craigslistJobCategories = []string{
	"/sof/", // software
	"/web/", // web/info design
	"/cps/", // computer
	"/tch/", // tech support
	"/eng/", // engineering
	"/sci/", // science/biotech
	"/jjj/", // all jobs
	"/bus/", // business
	"/ofc/", // office/admin
	"/mnu/", // manufacturing
	"/sls/", // sales
	"/trp/", // transportation
	"/med/", // healthcare
	"/hea/", // healthcare (alt)
	"/edu/", // education
	"/acc/", // accounting/finance
	"/fbh/", // food/beverage/hospitality
	"/lab/", // general labor
	"/sec/", // security
	"/ret/", // retail/wholesale
	"/mar/", // marketing/pr/ad
	"/hum/", // human resources
	"/lgl/", // legal/paralegal
	"/npo/", // nonprofit
	"/rej/", // real estate
	"/spa/", // salon/spa/fitness
	"/gov/", // government
	"/art/", // art/media/design
	"/wri/", // writing/editing
}

// SearchCraigslistJobs searches Craigslist job listings via SearXNG site: query.
// Uses broad site:craigslist.org search, then filters to only individual listing
// URLs (containing /<id>.html) in job-related categories.
func SearchCraigslistJobs(ctx context.Context, query, location string, limit int) ([]engine.SearxngResult, error) {
	engine.IncrCraigslistRequests()

	searxQuery := query + " jobs " + craigslistSiteSearch
	if location != "" {
		searxQuery = query + " " + location + " jobs " + craigslistSiteSearch
	}

	searxResults, err := engine.SearchSearXNG(ctx, searxQuery, "en", "", engine.DefaultSearchEngine)
	if err != nil {
		slog.Warn("craigslist: SearXNG error", slog.Any("error", err))
	}

	var results []engine.SearxngResult
	for _, r := range searxResults {
		if !craigslistListingRe.MatchString(r.URL) {
			continue
		}
		if !isCraigslistJobCategory(r.URL) {
			continue
		}
		r.Content = "**Source:** Craigslist\n\n" + r.Content
		r.Score = 0.7
		results = append(results, r)
	}

	if len(results) > limit {
		results = results[:limit]
	}

	slog.Debug("craigslist: search complete",
		slog.Int("raw", len(searxResults)),
		slog.Int("listings", len(results)))
	return results, nil
}

// isCraigslistJobCategory checks if a URL belongs to a job-related category.
func isCraigslistJobCategory(url string) bool {
	for _, cat := range craigslistJobCategories {
		if strings.Contains(url, cat) {
			return true
		}
	}
	return false
}
