package jobs

import (
	"context"
	"encoding/xml"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
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
	"/sof/", "/web/", "/cps/", "/tch/", "/eng/", "/sci/",
	"/jjj/", "/bus/", "/ofc/", "/mnu/", "/sls/", "/trp/",
	"/med/", "/hea/", "/edu/", "/acc/", "/fbh/", "/lab/",
	"/sec/", "/ret/", "/mar/", "/hum/", "/lgl/", "/npo/",
	"/rej/", "/spa/", "/gov/", "/art/", "/wri/",
}

// --- RSS types ---

type craigslistRSS struct {
	XMLName xml.Name             `xml:"rss"`
	Channel craigslistRSSChannel `xml:"channel"`
}

type craigslistRSSChannel struct {
	Items []craigslistRSSItem `xml:"item"`
}

type craigslistRSSItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	Description string `xml:"description"`
	Date        string `xml:"date"` // dc:date — ISO 8601
}

// --- Region mapping ---

// craigslistRegions maps common location keywords to Craigslist subdomains.
var craigslistRegions = map[string]string{
	"san francisco": craigslistCitySFBay, "sf": craigslistCitySFBay, "bay area": craigslistCitySFBay,
	"oakland": craigslistCitySFBay, "san jose": craigslistCitySFBay, "silicon valley": craigslistCitySFBay,
	"new york": craigslistCityNewYork, "nyc": craigslistCityNewYork, "manhattan": craigslistCityNewYork, "brooklyn": craigslistCityNewYork,
	"los angeles": "losangeles", "la": "losangeles",
	"chicago": "chicago",
	"seattle": "seattle", "tacoma": "seattle",
	"boston": "boston",
	"denver": "denver",
	"austin": "austin",
	"portland": "portland",
	"dallas": "dallas", "fort worth": "dallas",
	"houston": "houston",
	"atlanta": "atlanta",
	"miami": "miami",
	"phoenix": "phoenix",
	"philadelphia": "philadelphia", "philly": "philadelphia",
	"detroit": "detroit",
	"minneapolis": "minneapolis",
	"san diego": "sandiego",
	"washington": "washingtondc", "dc": "washingtondc",
	"las vegas": "lasvegas", "vegas": "lasvegas",
}

func resolveRegion(location string) string {
	loc := strings.ToLower(strings.TrimSpace(location))
	if region, ok := craigslistRegions[loc]; ok {
		return region
	}
	for key, region := range craigslistRegions {
		if strings.Contains(loc, key) {
			return region
		}
	}
	return "www"
}

// --- RSS fetch ---

// fetchCraigslistRSS fetches and parses the Craigslist RSS feed for a given query/location.
// Requires BrowserClient (Craigslist blocks non-browser TLS fingerprints).
func fetchCraigslistRSS(ctx context.Context, query, location string, limit int) ([]engine.SearxngResult, error) {
	region := resolveRegion(location)
	feedURL := fmt.Sprintf("https://%s.craigslist.org/search/jjj?query=%s&format=rss",
		region, url.QueryEscape(query))

	ctx, cancel := context.WithTimeout(ctx, engine.Cfg.FetchTimeout)
	defer cancel()

	headers := engine.ChromeHeaders()
	headers["accept"] = "application/rss+xml, application/xml, text/xml"

	data, err := engine.RetryDo(ctx, engine.DefaultRetryConfig, func() ([]byte, error) {
		d, _, status, e := engine.Cfg.BrowserClient.Do("GET", feedURL, headers, nil)
		if e != nil {
			return nil, e
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("craigslist RSS status %d", status)
		}
		return d, nil
	})
	if err != nil {
		return nil, fmt.Errorf("craigslist RSS fetch: %w", err)
	}

	return parseCraigslistRSS(data, limit)
}

func parseCraigslistRSS(body []byte, limit int) ([]engine.SearxngResult, error) {
	var rss craigslistRSS
	if err := xml.Unmarshal(body, &rss); err != nil {
		return nil, fmt.Errorf("craigslist RSS parse: %w", err)
	}

	var results []engine.SearxngResult
	for _, item := range rss.Channel.Items {
		if item.Title == "" || item.Link == "" {
			continue
		}

		posted := ""
		if len(item.Date) >= 10 {
			posted = item.Date[:10]
		}

		content := "**Source:** Craigslist"
		if posted != "" {
			content += " | **Posted:** " + posted
		}
		if item.Description != "" {
			content += "\n\n" + engine.TruncateAtWord(item.Description, 300)
		}

		results = append(results, engine.SearxngResult{
			Title:   item.Title,
			Content: content,
			URL:     item.Link,
			Score:   0.8,
		})

		if len(results) >= limit {
			break
		}
	}

	return results, nil
}

// --- Main search function ---

// SearchCraigslistJobs searches Craigslist job listings.
// Primary: RSS feed via BrowserClient (structured data, more results).
// Fallback: discoverJobURLs (go-engine DIRECT primary + SearXNG additive) when
// BrowserClient is unavailable or RSS fails. SEARXNG_URL is unset in prod, so
// a bare SearchSearXNG-only fallback would return nil,nil silently — same class
// fixed in #53 for ATS/YC/Indeed.
func SearchCraigslistJobs(ctx context.Context, query, location string, limit int) ([]engine.SearxngResult, error) {
	engine.IncrCraigslistRequests()

	if engine.Cfg.BrowserClient != nil {
		results, err := fetchCraigslistRSS(ctx, query, location, limit)
		if err != nil {
			slog.Warn("craigslist: RSS fetch failed, falling back to discovery",
				slog.Any("error", err))
		} else if len(results) > 0 {
			slog.Debug("craigslist: RSS search complete", slog.Int("results", len(results)))
			return results, nil
		}
	}

	// Fallback: go-engine DIRECT (primary, always-on) + SearXNG (additive).
	// discoverJobURLs fans out to both sources and dedupes by URL — mirroring
	// the ATS/YC/Indeed routing fix in #53.
	searxQuery := query + " jobs " + craigslistSiteSearch
	if location != "" {
		searxQuery = query + " " + location + " jobs " + craigslistSiteSearch
	}

	discovered := discoverJobURLs(ctx, searxQuery)

	var results []engine.SearxngResult
	for _, r := range discovered {
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

	slog.Debug("craigslist: discovery fallback complete",
		slog.Int("raw", len(discovered)),
		slog.Int("listings", len(results)))
	return results, nil
}

func isCraigslistJobCategory(u string) bool {
	for _, cat := range craigslistJobCategories {
		if strings.Contains(u, cat) {
			return true
		}
	}
	return false
}
