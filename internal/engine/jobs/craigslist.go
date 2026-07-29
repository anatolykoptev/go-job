package jobs

import (
	"context"
	"encoding/xml"
	"errors"
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

// Craigslist serves RSS 1.0 (RDF/XML), NOT RSS 2.0. The root element is
// <rdf:RDF>, and <item> elements are direct children of the root (not nested
// inside <channel>). The date is in the Dublin Core namespace: <dc:date>.
//
// The previous parser expected <rss><channel><item> with <date> (no namespace),
// which silently parsed 0 items from the real feed — a format mismatch that
// looked identical to "genuine empty" and was invisible at INFO log level.

type craigslistRSS struct {
	// Craigslist serves RSS 1.0: root is <rdf:RDF> in the
	// "http://www.w3.org/1999/02/22-rdf-syntax-ns#" namespace. Go's xml
	// package matches on namespace URI + local name, NOT on the prefix, so
	// the tag must use the full URI. Matching on the bare prefix "rdf:RDF"
	// fails with "expected <rdf:RDF> but have <RDF>" because the decoder
	// resolves the prefix to the local name "RDF".
	XMLName xml.Name            `xml:"http://www.w3.org/1999/02/22-rdf-syntax-ns# RDF"`
	Items   []craigslistRSSItem `xml:"http://purl.org/rss/1.0/ item"`
}

type craigslistRSSItem struct {
	// All RSS 1.0 elements (title, link, description) live in the
	// "http://purl.org/rss/1.0/" namespace; dc:date in Dublin Core.
	Title       string `xml:"http://purl.org/rss/1.0/ title"`
	Link        string `xml:"http://purl.org/rss/1.0/ link"`
	Description string `xml:"http://purl.org/rss/1.0/ description"`
	Date        string `xml:"http://purl.org/dc/elements/1.1/ date"`
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
	"boston":   "boston",
	"denver":   "denver",
	"austin":   "austin",
	"portland": "portland",
	"dallas":   "dallas", "fort worth": "dallas",
	"houston":      "houston",
	"atlanta":      "atlanta",
	"miami":        "miami",
	"phoenix":      "phoenix",
	"philadelphia": "philadelphia", "philly": "philadelphia",
	"detroit":     "detroit",
	"minneapolis": "minneapolis",
	"san diego":   "sandiego",
	"washington":  "washingtondc", "dc": "washingtondc",
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

// --- Transport seams (overridable in tests) ---

// craigslistStealthFetch is the Tier-1 transport: go-stealth Chrome-TLS client.
// In direct mode, engine.Cfg.BrowserClient is the no-proxy stealth client
// (config.go falls back to fetcherProxy.DirectClient() when BrowserClient() is nil).
//
// Returns (status, body, err) so the caller can distinguish a 403 block
// (status=403, err=nil) from a transport error (err!=nil).
var craigslistStealthFetch = func(ctx context.Context, feedURL string, headers map[string]string) (status int, body []byte, err error) {
	if engine.Cfg.BrowserClient == nil {
		return 0, nil, errors.New("craigslist: stealth client not configured")
	}
	body, _, status, err = engine.Cfg.BrowserClient.DoCtx(ctx, "GET", feedURL, headers, nil)
	return status, body, err
}

// craigslistOxBrowserFetch is the Tier-2 transport: ox-browser /fetch-smart.
// Reuses engine.FetchProxyBody, which owns the stealth → ox-browser cascade
// wired in config.go via fetch.WithOxBrowser(Config.OxBrowserURL). When the
// stealth tier already failed, FetchProxyBody's direct-first classifier
// escalates to the ox-browser /fetch-smart fallback.
//
// Returns (status, body, err) matching the stealth tier signature.
var craigslistOxBrowserFetch = func(ctx context.Context, feedURL string, headers map[string]string) (status int, body []byte, err error) {
	body, err = engine.FetchProxyBody(ctx, feedURL, headers)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, body, nil
}

// --- RSS fetch ---

// errCraigslistBlocked is returned when every tier was refused (403/429/challenge).
// Distinct from a transport error or a genuine empty feed.
var errCraigslistBlocked = errors.New("craigslist: blocked (all tiers refused)")

// fetchCraigslistRSS fetches and parses the Craigslist RSS feed using a two-tier
// transport ladder:
//  1. Stealth (go-stealth Chrome-TLS) — cheap, no browser, right tier for a static XML feed.
//  2. ox-browser /fetch-smart — anti-bot fallback, reuses the FetchProxyBody cascade.
//
// On 403/429 from stealth, escalates to ox-browser. If both refuse, returns
// errCraigslistBlocked. If a tier errors (transport/parse/deadline), returns
// the wrapped error. If a tier succeeds, returns parsed results (which may be
// empty if the feed genuinely has no matching listings).
func fetchCraigslistRSS(ctx context.Context, query, location string, limit int) ([]engine.SearxngResult, error) {
	region := resolveRegion(location)
	feedURL := fmt.Sprintf("https://%s.craigslist.org/search/jjj?query=%s&format=rss",
		region, url.QueryEscape(query))

	ctx, cancel := context.WithTimeout(ctx, engine.Cfg.FetchTimeout)
	defer cancel()

	headers := engine.ChromeHeaders()
	headers["accept"] = "application/rss+xml, application/xml, text/xml"

	// Tier 1: stealth Chrome-TLS.
	status, body, err := craigslistStealthFetch(ctx, feedURL, headers)
	if err == nil && status == http.StatusOK && len(body) > 0 {
		results, parseErr := parseCraigslistRSS(body, limit)
		if parseErr != nil {
			slog.Warn("craigslist: stealth RSS parse failed",
				slog.String("url", feedURL),
				slog.Any("error", parseErr))
			return nil, fmt.Errorf("craigslist stealth parse: %w", parseErr)
		}
		return results, nil
	}

	// Log the stealth tier failure before escalating.
	if err != nil {
		slog.Warn("craigslist: stealth tier failed, escalating to ox-browser",
			slog.String("url", feedURL),
			slog.Int("status", status),
			slog.Any("error", err))
	} else {
		slog.Warn("craigslist: stealth tier blocked, escalating to ox-browser",
			slog.String("url", feedURL),
			slog.Int("status", status))
	}

	// Tier 2: ox-browser /fetch-smart (reuses FetchProxyBody cascade).
	oxStatus, oxBody, oxErr := craigslistOxBrowserFetch(ctx, feedURL, headers)
	if oxErr == nil && oxStatus == http.StatusOK && len(oxBody) > 0 {
		results, parseErr := parseCraigslistRSS(oxBody, limit)
		if parseErr != nil {
			slog.Warn("craigslist: ox-browser RSS parse failed",
				slog.String("url", feedURL),
				slog.Any("error", parseErr))
			return nil, fmt.Errorf("craigslist ox-browser parse: %w", parseErr)
		}
		return results, nil
	}

	// Both tiers failed. Log at WARN with both tier outcomes.
	if oxErr != nil {
		slog.Warn("craigslist: both tiers exhausted (stealth + ox-browser)",
			slog.String("url", feedURL),
			slog.Int("stealth_status", status),
			slog.Int("ox_status", oxStatus),
			slog.Any("stealth_error", err),
			slog.Any("ox_error", oxErr))
	} else {
		slog.Warn("craigslist: both tiers refused (stealth + ox-browser)",
			slog.String("url", feedURL),
			slog.Int("stealth_status", status),
			slog.Int("ox_status", oxStatus))
	}

	return nil, fmt.Errorf("%w (stealth status=%d, ox-browser status=%d)", errCraigslistBlocked, status, oxStatus)
}

func parseCraigslistRSS(body []byte, limit int) ([]engine.SearxngResult, error) {
	var rss craigslistRSS
	if err := xml.Unmarshal(body, &rss); err != nil {
		return nil, fmt.Errorf("craigslist RSS parse: %w", err)
	}

	var results []engine.SearxngResult
	for _, item := range rss.Items {
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

// SearchCraigslistJobs searches Craigslist job listings via a two-tier transport
// ladder (stealth Chrome-TLS → ox-browser /fetch-smart), with a discovery
// fallback (go-search/SearXNG) as a last resort.
//
// Error semantics (Task 1 — never report success when nothing was fetched):
//   - every tier refused (403/429/challenge) → returns a blocked error
//   - a tier errored (transport, parse, deadline) → returns the wrapped error
//   - a tier genuinely ran and Craigslist has no matching listings → (nil, nil)
//
// The sibling branch fix/job-search-source-status adds a blocked/failed/skipped
// outcome vocabulary to JobSearchOutput but has NOT landed on main. Until it
// does, callers classify via errors.Is(err, errCraigslistBlocked) and the
// platform_results_total metric reads outcome=error (not outcome=empty) for
// blocked paths — which is the fix for the shipped defect.
func SearchCraigslistJobs(ctx context.Context, query, location string, limit int) ([]engine.SearxngResult, error) {
	engine.IncrCraigslistRequests()

	// Tier 1+2: stealth → ox-browser (via fetchCraigslistRSS).
	results, err := fetchCraigslistRSS(ctx, query, location, limit)
	if err == nil {
		if len(results) > 0 {
			slog.Debug("craigslist: RSS search complete", slog.Int("results", len(results)))
			return results, nil
		}
		// Genuine empty: RSS fetched OK, no matching listings.
		slog.Debug("craigslist: RSS returned no listings",
			slog.String("query", query),
			slog.String("location", location))
		return nil, nil
	}

	// RSS failed (blocked or errored). Try discovery as a last resort.
	slog.Warn("craigslist: RSS failed, trying discovery fallback",
		slog.String("query", query),
		slog.String("location", location),
		slog.Any("error", err))

	searxQuery := query + " jobs " + craigslistSiteSearch
	if location != "" {
		searxQuery = query + " " + location + " jobs " + craigslistSiteSearch
	}

	discovered := discoverJobURLs(ctx, searxQuery)

	var discResults []engine.SearxngResult
	for _, r := range discovered {
		if !craigslistListingRe.MatchString(r.URL) {
			continue
		}
		if !isCraigslistJobCategory(r.URL) {
			continue
		}
		r.Content = "**Source:** Craigslist\n\n" + r.Content
		r.Score = 0.7
		discResults = append(discResults, r)
	}
	if len(discResults) > limit {
		discResults = discResults[:limit]
	}

	if len(discResults) > 0 {
		slog.Debug("craigslist: discovery fallback complete",
			slog.Int("raw", len(discovered)),
			slog.Int("listings", len(discResults)))
		return discResults, nil
	}

	// All tiers exhausted: RSS blocked/errored AND discovery found nothing.
	// Return the RSS error (which is errCraigslistBlocked or a wrapped tier
	// error) — NOT (nil, nil), which is the defect that shipped as
	// outcome=empty with error=0.
	slog.Warn("craigslist: all tiers exhausted (RSS failed, discovery empty)",
		slog.String("query", query),
		slog.String("location", location),
		slog.Int("discovered_raw", len(discovered)),
		slog.Any("rss_error", err))
	return nil, err
}

func isCraigslistJobCategory(u string) bool {
	for _, cat := range craigslistJobCategories {
		if strings.Contains(u, cat) {
			return true
		}
	}
	return false
}
