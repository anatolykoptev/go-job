package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/html"

	"github.com/anatolykoptev/go_job/internal/engine"
)

const craigslistSiteSearch = "site:craigslist.org"

// craigslistListingRe matches individual Craigslist job posting URLs
// (e.g. sfbay.craigslist.org/pen/sof/d/san-mateo-senior-engineer/7856959859.html).
var craigslistListingRe = regexp.MustCompile(`craigslist\.org/.+/\d+\.html`)

// craigslistJobCategories are the Craigslist sections that contain job postings.
// Used by the discovery fallback to filter SearXNG results to job-category URLs.
var craigslistJobCategories = []string{
	"/sof/", "/web/", "/cps/", "/tch/", "/eng/", "/sci/",
	"/jjj/", "/bus/", "/ofc/", "/mnu/", "/sls/", "/trp/",
	"/med/", "/hea/", "/edu/", "/acc/", "/fbh/", "/lab/",
	"/sec/", "/ret/", "/mar/", "/hum/", "/lgl/", "/npo/",
	"/rej/", "/spa/", "/gov/", "/art/", "/wri/",
	"/trd/", "/csr/",
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

// resolveRegion maps a free-text location to a Craigslist area slug. Returns
// (region, true) on a match, ("", false) when the location is not in the
// craigslistRegions map and no substring key matches.
//
// The previous implementation returned the literal sentinel "www" for any
// unmatched location. That was harmless when the region was a SUBDOMAIN
// (https://www.craigslist.org/...) but this branch moved it into the PATH
// (https://www.craigslist.org/search/area/<region>?...), where "www" is a
// bogus area slug that 404s (measured live: /search/area/www?cat=jjj&... →
// 404 "Page Not Found", identical to /search/area/bogusregion). Returning
// false lets the caller fail the direct tiers with an error that NAMES the
// unmapped location, instead of silently building a 404 URL that is
// indistinguishable from an IP block in the logs.
//
// Non-determinism from the map-iteration substring pass is tracked in #347
// and is NOT fixed here; only the sentinel-in-path bug is.
func resolveRegion(location string) (string, bool) {
	loc := strings.ToLower(strings.TrimSpace(location))
	if loc == "" {
		return "", false
	}
	if region, ok := craigslistRegions[loc]; ok {
		return region, true
	}
	for key, region := range craigslistRegions {
		if strings.Contains(loc, key) {
			return region, true
		}
	}
	return "", false
}

// --- HTML search URL ---

// craigslistHTMLSearchURL builds the no-JS HTML search URL that Craigslist
// serves to crawlers. Measured: this URL returns 200 with 115+ listings from
// a plain curl (no stealth, no proxy, no browser), while the RSS endpoint
// (format=rss) is IP-blocked (403) from the same host.
//
// URL shape (after following the redirect from the regional host):
//
//	https://www.craigslist.org/search/area/{region}?cat=jjj&query={query}
//
// cat=jjj is the "all jobs" category — it includes every job sub-category
// (fbh, lab, trd, csr, etc.), so a single fetch covers the full job board.
func craigslistHTMLSearchURL(region, query string) string {
	return fmt.Sprintf("https://www.craigslist.org/search/area/%s?cat=jjj&query=%s",
		region, url.QueryEscape(query))
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

// craigslistOxFetchFetch is the HTML Tier-2 transport: ox-browser POST /fetch.
// /fetch is a fast wreq+BoringSSL fetch with Chrome TLS/JA3 impersonation,
// ox-browser's proxy pool and CF solver behind it, returning the RAW body —
// no Readability, no extraction. It serves the SAME static markup as a plain
// GET (li.cl-static-search-result), so both tiers share one parser.
//
// ox-browser /fetch passes ordinary statuses through (404 confirmed) but
// ABSORBS 403 — it reads 403 as anti-bot, escalates into its proxy pool and
// CF solver, and on exhaustion returns wrapper 502 / status 0 / a solver
// error string. A real Craigslist block therefore arrives as an exhausted-
// cascade error, never as a 403. The classification below encodes that.
//
// Returns (status, body, err) matching the stealth tier signature.
//   - success: (200, body, nil)
//   - blocked: (0, nil, errCraigslistBlocked) — inner 403/429, cf_detected,
//     or wrapper 502 with a solver/cf_clearance cascade error
//   - transport error: (0, nil, wrappedErr) — POST failed, connect error,
//     deadline, or a non-block inner status (404, 500, …)
var craigslistOxFetchFetch = func(ctx context.Context, pageURL string, headers map[string]string) (status int, body []byte, err error) {
	fetchURL := strings.TrimRight(engine.Cfg.OxBrowserURL, "/") + "/fetch"
	payload, err := json.Marshal(map[string]any{
		"url":     pageURL,
		"headers": headers,
		"timeout": int(engine.Cfg.FetchTimeout.Seconds()),
	})
	if err != nil {
		return 0, nil, fmt.Errorf("craigslist ox-browser /fetch marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fetchURL, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, fmt.Errorf("craigslist ox-browser /fetch request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// H4: route through engine.Cfg.HTTPClient (PF-13: MaxIdleConns=100,
	// MaxConnsPerHost=10, MaxIdleConnsPerHost=10, IdleConnTimeout=90s) instead
	// of http.DefaultClient (MaxIdleConnsPerHost=2, unbounded MaxConnsPerHost).
	// A platform=all fan-out against http.DefaultClient recreates the exact
	// FD-exhaustion condition PF-13 fixed. go-stealth's OxBrowserClient.post()
	// already owns this marshal→POST→read→unmarshal boilerplate against a
	// configured client (60s timeout, optional proxy); a Fetch() method reusing
	// it is the preferred fix but requires a cross-repo tag+vendor-bump of
	// go-stealth, out of scope for this PR — see report.
	if engine.Cfg.HTTPClient == nil {
		return 0, nil, errors.New("craigslist: ox-browser /fetch: HTTPClient not configured")
	}
	resp, err := engine.Cfg.HTTPClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("craigslist ox-browser /fetch: %w", err)
	}
	defer resp.Body.Close()
	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return 0, nil, fmt.Errorf("craigslist ox-browser /fetch body: %w", readErr)
	}

	var oxResp oxFetchResponse
	if jsonErr := json.Unmarshal(respBody, &oxResp); jsonErr != nil {
		return 0, nil, fmt.Errorf("craigslist ox-browser /fetch decode: %w", jsonErr)
	}

	// Wrapper 200 — ox-browser returned a response from the target.
	if resp.StatusCode == http.StatusOK {
		if oxResp.CfDetected {
			return 0, nil, errCraigslistBlocked
		}
		if oxResp.Status == http.StatusForbidden || oxResp.Status == http.StatusTooManyRequests {
			return 0, nil, errCraigslistBlocked
		}
		if oxResp.Status == http.StatusOK && oxResp.Body != "" {
			return http.StatusOK, []byte(oxResp.Body), nil
		}
		return 0, nil, fmt.Errorf("craigslist ox-browser /fetch: inner status %d", oxResp.Status)
	}

	// Wrapper non-200 (typically 502) — ox-browser's own error. The error
	// string names the failure mode:
	//   "proxy pool error: solver failed: ..." — exhausted anti-bot cascade
	//   "proxy pool error: solver negcache: ..." — per-domain solver cooldown
	//   "request failed: ... client error (Connect)" — connect error
	//
	// STRING MATCH against a foreign service (ox-browser crates/http/src/
	// error.rs:19 HttpError::ProxyPool, middleware_solver.rs:107,122). The
	// "solver" substring covers both ProxyPool-solver variants and does not
	// match Webshare API errors or wreq connect errors. Fragile by nature —
	// if ox-browser changes its error wording, this match must be updated.
	if isOxBrowserCascadeError(oxResp.Error) {
		return 0, nil, errCraigslistBlocked
	}
	return 0, nil, fmt.Errorf("craigslist ox-browser /fetch: wrapper %d: %s",
		resp.StatusCode, oxResp.Error)
}

// oxFetchResponse is the JSON body of ox-browser's POST /fetch response.
// Source: ox-browser crates/js/src/fetch.rs:24-35.
type oxFetchResponse struct {
	Status     int               `json:"status"`
	Headers    map[string]string `json:"headers"`
	Body       string            `json:"body"`
	CfDetected bool              `json:"cf_detected"`
	CfType     string            `json:"cf_type,omitempty"`
	ElapsedMs  int64             `json:"elapsed_ms"`
	Error      string            `json:"error,omitempty"`
}

// isOxBrowserCascadeError returns true if the ox-browser error string names an
// exhausted anti-bot cascade (solver failure or per-domain solver cooldown).
// Pointed at ox-browser crates/http/src/middleware_solver.rs:107,122 — both
// construct HttpError::ProxyPool with a "solver ..." prefix. The "cf_clearance"
// substring appears inside solver error messages (e.g. "timeout waiting for
// cf_clearance") and is kept as a secondary signal.
func isOxBrowserCascadeError(oxErr string) bool {
	return strings.Contains(oxErr, "solver") || strings.Contains(oxErr, "cf_clearance")
}

// craigslistOxBrowserFetch is the RSS Tier-3 transport: ox-browser /fetch-smart.
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

// --- Error sentinels ---

// errCraigslistBlocked is returned when any tier detected a genuine anti-bot
// block — HTTP 403/429 from stealth, or errCraigslistBlocked from the ox-browser
// /fetch tier (inner 403/429, cf_detected, or an exhausted solver cascade).
// Distinct from a transport error or a genuine empty result. A transport error
// or parse failure on every tier (with no block signal) means the joined tier
// errors are returned instead, so context.DeadlineExceeded and similar reach
// engine.PlatformOutcome undowngraded.
var errCraigslistBlocked = errors.New("craigslist: blocked (anti-bot refusal detected)")

// errCraigslistUnmapped is returned when resolveRegion cannot map the
// free-text location to a Craigslist area slug. Distinct from a transport
// error or a block: the connector never reached the network. Surfaced as
// reason="unmapped" on the discovery-fallback counter so a default-empty
// q.Location (the common case via adapters.go) is distinguishable from a
// real transport failure (reason="failed") in the metric noise floor.
var errCraigslistUnmapped = errors.New("craigslist: location not mapped to a Craigslist area")

// --- HTML parsing helpers ---

// hasClassToken returns true if the node's "class" attribute contains token as a
// whitespace-separated token (NOT a substring — "cl-search-result" does not
// match "cl-static-search-result"). The package-level hasClass (linkedin.go)
// uses strings.Contains, which would false-positive on class prefixes; this
// token-based variant is required for the Craigslist two-tier selectors.
func hasClassToken(n *html.Node, token string) bool {
	if n.Type != html.ElementNode {
		return false
	}
	for _, attr := range n.Attr {
		if attr.Key == "class" {
			for _, c := range strings.Fields(attr.Val) {
				if c == token {
					return true
				}
			}
		}
	}
	return false
}

// nodeText returns the concatenated text content of a node's descendants,
// with HTML entities already decoded by html.Parse.
func nodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			b.WriteString(node.Data)
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.TrimSpace(b.String())
}

// findFirstDescendant returns the first descendant element with the given class token.
func findFirstDescendantC(n *html.Node, class string) *html.Node {
	var walk func(*html.Node) *html.Node
	walk = func(node *html.Node) *html.Node {
		if node.Type == html.ElementNode && hasClass(node, class) {
			return node
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			if found := walk(c); found != nil {
				return found
			}
		}
		return nil
	}
	return walk(n)
}

// findFirstAnchor returns the first descendant <a> element with an href.
func findFirstAnchor(n *html.Node) *html.Node {
	var walk func(*html.Node) *html.Node
	walk = func(node *html.Node) *html.Node {
		if node.Type == html.ElementNode && node.Data == "a" && getAttr(node, "href") != "" {
			return node
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			if found := walk(c); found != nil {
				return found
			}
		}
		return nil
	}
	return walk(n)
}

// --- HTML parser ---

// parseCraigslistHTML parses the Craigslist static search-results HTML served
// by both the stealth tier (plain GET) and the ox-browser /fetch tier (raw
// body). Both return the same no-JS markup: each result is a
// <li class="cl-static-search-result" title="..."> containing an <a href> and
// a <div class="location">.
//
// Genuine-empty vs wrong-format discriminator (measured on both a 112-result
// page and a 0-result page):
//   - <ol class="cl-static-search-results"> occurs exactly once on both pages.
//   - The 0-result page carries that <ol> with zero li.cl-static-search-result
//     children (it has li.cl-static-hub-links instead — "see also" links).
//
// So:
//   - <ol> present, ≥1 li.cl-static-search-result → results
//   - <ol> present, zero li → genuine empty → (nil, nil)
//   - <ol> absent → the response is not a Craigslist search page → ErrParse
//
// The substring trap: the literal "cl-static-search-result" appears 3 times in
// the page's own CSS (ol.cl-static-search-results, .no-js ol.cl-static-search-
// results) even on a zero-result page. A substring count returns 3 for an empty
// page. hasClassToken matches the element via whitespace-separated class token,
// not substring — "cl-static-search-results" (plural, the <ol>) does not match
// "cl-static-search-result" (singular, the <li>).
func parseCraigslistHTML(body []byte, limit int) ([]engine.SearxngResult, error) {
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("craigslist HTML parse: %w: %w", ErrParse, err)
	}

	// Discriminator: <ol class="cl-static-search-results"> must be present.
	hasResultsOL := false
	var findOL func(*html.Node) bool
	findOL = func(n *html.Node) bool {
		if n.Type == html.ElementNode && n.Data == "ol" && hasClassToken(n, "cl-static-search-results") {
			return true
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if findOL(c) {
				return true
			}
		}
		return false
	}
	hasResultsOL = findOL(doc)
	if !hasResultsOL {
		return nil, fmt.Errorf("craigslist HTML parse: %w: no ol.cl-static-search-results (not a Craigslist search page)", ErrParse)
	}

	// Collect li.cl-static-search-result elements.
	var results []engine.SearxngResult
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "li" && hasClassToken(n, "cl-static-search-result") {
			title := getAttr(n, "title")
			if title == "" {
				if td := findFirstDescendantC(n, "title"); td != nil {
					title = nodeText(td)
				}
			}
			href := ""
			if a := findFirstAnchor(n); a != nil {
				href = getAttr(a, "href")
			}
			location := ""
			if loc := findFirstDescendantC(n, "location"); loc != nil {
				location = nodeText(loc)
			}
			if title == "" || href == "" {
				return
			}
			results = append(results, buildCraigslistResult(title, href, location, ""))
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	// <ol> present, zero li.cl-static-search-result → genuine empty.
	if len(results) == 0 {
		return nil, nil
	}
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

// buildCraigslistResult assembles a SearxngResult from the parsed fields.
// title is already entity-decoded by html.Parse; location is also decoded.
func buildCraigslistResult(title, href, location, posted string) engine.SearxngResult {
	content := "**Source:** Craigslist"
	if location != "" {
		content += " | **Location:** " + location
	}
	if posted != "" {
		content += " | **Posted:** " + posted
	}
	md := map[string]string{"source": "craigslist"}
	if location != "" {
		md["location"] = location
	}
	return engine.SearxngResult{
		Title:    title,
		Content:  content,
		URL:      href,
		Score:    0.8,
		Metadata: md,
	}
}

// --- RSS fetch ---

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
	region, ok := resolveRegion(location)
	if !ok {
		return nil, fmt.Errorf("%w: %q", errCraigslistUnmapped, location)
	}
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

	// 3a: return errCraigslistBlocked ONLY when both tiers returned a genuine
	// refusal status (403/429). If either tier had a transport error (dial
	// timeout, connection refused, context deadline), return the wrapped tier
	// error(s) WITHOUT the sentinel — so context.DeadlineExceeded reaches
	// engine.PlatformOutcome undowngraded (outcome=timeout, not outcome=error),
	// and an ox-browser outage does not read to an operator as "Craigslist
	// blocked our IP".
	stealthRefused := err == nil && isRefusalStatus(status)
	oxRefused := oxErr == nil && isRefusalStatus(oxStatus)
	if stealthRefused && oxRefused {
		// Join the sentinel WITH the status context so a block is still
		// distinguishable from a transport error in the logs (the bare
		// sentinel dropped the status codes). errors.Is(err, errCraigslistBlocked)
		// still matches; the status context survives to the operator.
		return nil, errors.Join(
			errCraigslistBlocked,
			fmt.Errorf("craigslist rss: stealth status=%d ox-browser status=%d", status, oxStatus),
		)
	}
	var tierErrs []error
	if err != nil {
		tierErrs = append(tierErrs, fmt.Errorf("craigslist rss stealth: %w", err))
	}
	if oxErr != nil {
		tierErrs = append(tierErrs, fmt.Errorf("craigslist rss ox-browser: %w", oxErr))
	}
	if len(tierErrs) == 0 {
		tierErrs = append(tierErrs, fmt.Errorf("craigslist rss: stealth status=%d ox-browser status=%d", status, oxStatus))
	}
	return nil, errors.Join(tierErrs...)
}

// isRefusalStatus returns true for HTTP status codes that represent a genuine
// anti-bot refusal (as opposed to a transport error or a successful response).
func isRefusalStatus(status int) bool {
	return status == http.StatusForbidden || status == http.StatusTooManyRequests
}

func parseCraigslistRSS(body []byte, limit int) ([]engine.SearxngResult, error) {
	var rss craigslistRSS
	if err := xml.Unmarshal(body, &rss); err != nil {
		return nil, fmt.Errorf("craigslist RSS parse: %w: %w", ErrParse, err)
	}

	var results []engine.SearxngResult
	for _, item := range rss.Items {
		if item.Title == "" || item.Link == "" {
			continue
		}

		// Craigslist wraps titles and descriptions in CDATA containing encoded
		// HTML entities; Go's xml decoder correctly leaves CDATA content literal,
		// so we must decode entities ourselves. Descriptions carry raw HTML,
		// so strip tags after unescaping.
		title := html.UnescapeString(item.Title)
		posted := ""
		if len(item.Date) >= 10 {
			posted = item.Date[:10]
		}
		desc := ""
		if item.Description != "" {
			desc = engine.CleanHTML(html.UnescapeString(item.Description))
		}

		content := "**Source:** Craigslist"
		if posted != "" {
			content += " | **Posted:** " + posted
		}
		if desc != "" {
			content += "\n\n" + engine.TruncateAtWord(desc, 300)
		}

		results = append(results, engine.SearxngResult{
			Title:    title,
			Content:  content,
			URL:      item.Link,
			Score:    0.8,
			Metadata: map[string]string{"source": "craigslist"},
		})

		if len(results) >= limit {
			break
		}
	}

	return results, nil
}

// --- HTML-first listing ladder ---

// tierOutcome records the result of a single tier attempt.
type tierOutcome struct {
	name    string // "html-static", "ox-fetch", "rss"
	err     error
	refused bool // HTTP 403/429 or errCraigslistBlocked — a genuine block
}

// fetchCraigslistListings is the HTML-first transport ladder:
//  1. HTML static (stealth Chrome-TLS) — li.cl-static-search-result, ~0.4s, no browser.
//  2. ox-browser POST /fetch — Chrome TLS/JA3 impersonation + proxy pool + CF solver,
//     returns the same static markup as tier 1. Skipped (and reported) when
//     engine.Cfg.OxBrowserURL is empty.
//  3. RSS (stealth → ox-browser /fetch-smart) — last tier; currently blocked for
//     Craigslist, its failure must not decide the blocked verdict.
//
// Escalation rules:
//   - A tier that returns results → stop, return them.
//   - A tier that returns genuine empty (nil, nil) → stop, return nil, nil.
//   - A tier refused (403/429/errCraigslistBlocked) → escalate.
//   - A tier transport error → record, escalate.
//   - A tier 200 but wrong format (ErrParse, no <ol>) → escalate.
//
// Error semantics:
//   - ANY tier detected a block (403/429/errCraigslistBlocked) → errCraigslistBlocked.
//     A confirmed block from any tier is the honest verdict — the other tiers
//     may have had transport errors, but the block signal is authoritative.
//   - No tier detected a block, all had transport/parse errors → joined errors
//     WITHOUT the sentinel, so context.DeadlineExceeded reaches PlatformOutcome
//     undowngraded (outcome=timeout, not outcome=error).
func fetchCraigslistListings(ctx context.Context, query, location string, limit int) ([]engine.SearxngResult, error) {
	region, ok := resolveRegion(location)
	if !ok {
		return nil, fmt.Errorf("%w: %q", errCraigslistUnmapped, location)
	}
	htmlURL := craigslistHTMLSearchURL(region, query)
	ctx, cancel := context.WithTimeout(ctx, engine.Cfg.FetchTimeout)
	defer cancel()

	htmlHeaders := engine.ChromeHeaders()
	htmlHeaders["accept"] = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"

	var outcomes []tierOutcome

	// --- Tier 1: HTML static (stealth) ---
	sStatus, sBody, sErr := craigslistStealthFetch(ctx, htmlURL, htmlHeaders)
	if sErr == nil && sStatus == http.StatusOK && len(sBody) > 0 {
		results, parseErr := parseCraigslistHTML(sBody, limit)
		if parseErr == nil {
			if len(results) > 0 {
				return results, nil
			}
			return nil, nil // genuine empty
		}
		// 200 but wrong format (no <ol>) → soft-block signal, escalate.
		slog.Warn("craigslist: HTML static parse failed (soft-block?), escalating",
			slog.String("url", htmlURL),
			slog.Any("error", parseErr))
		outcomes = append(outcomes, tierOutcome{name: "html-static", err: parseErr})
	} else {
		outcomes = append(outcomes, recordTransportFailure("html-static", sStatus, sErr))
	}

	// --- Tier 2: ox-browser POST /fetch ---
	if engine.Cfg.OxBrowserURL == "" {
		slog.Warn("craigslist: ox-browser /fetch tier skipped (OxBrowserURL empty)",
			slog.String("url", htmlURL))
	} else {
		oxStatus, oxBody, oxErr := craigslistOxFetchFetch(ctx, htmlURL, htmlHeaders)
		if oxErr == nil && oxStatus == http.StatusOK && len(oxBody) > 0 {
			results, parseErr := parseCraigslistHTML(oxBody, limit)
			if parseErr == nil {
				if len(results) > 0 {
					return results, nil
				}
				return nil, nil // genuine empty
			}
			slog.Warn("craigslist: ox-browser /fetch parse failed (soft-block?), escalating to RSS",
				slog.String("url", htmlURL),
				slog.Any("error", parseErr))
			outcomes = append(outcomes, tierOutcome{name: "ox-fetch", err: parseErr})
		} else {
			outcomes = append(outcomes, recordTransportFailure("ox-fetch", oxStatus, oxErr))
		}
	}

	// --- Tier 3: RSS (stealth → ox-browser /fetch-smart) ---
	rssResults, rssErr := fetchCraigslistRSS(ctx, query, location, limit)
	if rssErr == nil {
		if len(rssResults) > 0 {
			return rssResults, nil
		}
		return nil, nil // genuine empty (RSS 0-item feed)
	}
	outcomes = append(outcomes, tierOutcome{name: "rss", err: rssErr, refused: errors.Is(rssErr, errCraigslistBlocked)})

	// --- All tiers exhausted ---
	return synthesizeLadderError(outcomes)
}

// recordTransportFailure builds a tierOutcome from a non-200 or errored fetch.
func recordTransportFailure(name string, status int, err error) tierOutcome {
	o := tierOutcome{name: name}
	if err != nil {
		o.err = fmt.Errorf("craigslist %s: %w", name, err)
		// errCraigslistBlocked from the /fetch tier is a confirmed block.
		o.refused = errors.Is(err, errCraigslistBlocked)
		if o.refused {
			slog.Warn("craigslist: tier blocked, escalating",
				slog.String("tier", name),
				slog.Any("error", err))
		} else {
			slog.Warn("craigslist: tier transport error, escalating",
				slog.String("tier", name),
				slog.Int("status", status),
				slog.Any("error", err))
		}
		return o
	}
	// No error but non-200 → refusal.
	o.refused = isRefusalStatus(status)
	o.err = fmt.Errorf("craigslist %s refused: HTTP %d", name, status)
	slog.Warn("craigslist: tier refused, escalating",
		slog.String("tier", name),
		slog.Int("status", status))
	return o
}

// synthesizeLadderError builds the final error from all tier outcomes.
// Returns errCraigslistBlocked (joined with the tier errors) if ANY tier
// detected a block (403/429 or errCraigslistBlocked). Otherwise returns the
// joined tier errors WITHOUT the sentinel, preserving the underlying cause
// (context.DeadlineExceeded, parse error, etc.).
//
// The sentinel is JOINED with the tier errors, not returned alone, so that
// errors.Is(err, errCraigslistBlocked) still matches while the
// DeadlineExceeded / 404 / parse causes survive to engine.PlatformOutcome
// (outcome=timeout, not outcome=error) and to the operator. Returning the
// bare sentinel dropped errs — a 404 from a URL-construction bug was
// indistinguishable from an IP block, and a ladder timeout read as "error".
func synthesizeLadderError(outcomes []tierOutcome) ([]engine.SearxngResult, error) {
	if len(outcomes) == 0 {
		return nil, errCraigslistBlocked
	}
	anyRefused := false
	var errs []error
	for _, o := range outcomes {
		if o.err != nil {
			errs = append(errs, o.err)
		}
		if o.refused {
			anyRefused = true
		}
	}
	if anyRefused {
		return nil, errors.Join(append(errs, errCraigslistBlocked)...)
	}
	return nil, errors.Join(errs...)
}

// --- Default location resolution ---

// craigslistProfileLocation reads the operator's resume profile location.
// It is the first fallback when a caller supplies no explicit location:
// Craigslist is region-scoped, so an empty location has nowhere to go, and the
// operator's own region (resume_persons.location) is the honest default.
//
// Overridable in tests via the same function-variable pattern as
// craigslistStealthFetch / craigslistOxFetchFetch — production reads the real
// profile through GetResumeProfile (the canonical accessor), tests inject a
// fixed value so the fallback path is exercised without a database.
var craigslistProfileLocation = func(ctx context.Context) string {
	prof, err := GetResumeProfile(ctx, "")
	if err != nil || prof == nil {
		return ""
	}
	return prof.Location
}

// resolveCraigslistDefaultLocation picks a location to use when the caller
// supplied none, in this order:
//  1. the operator's resume profile location (resume_persons.location);
//  2. engine.Cfg.CraigslistDefaultLocation (a deployment default for
//     installations with no profile);
//  3. "" — when both are empty, the caller keeps the current
//     errCraigslistUnmapped behaviour (fail rather than silently search the
//     wrong city).
//
// The resolved value is still subject to resolveRegion downstream: a profile
// location that does not map to a Craigslist area (e.g. "Remote") surfaces
// errCraigslistUnmapped, which is the honest outcome — the fallback substitutes
// a location, it does not guarantee the location maps.
func resolveCraigslistDefaultLocation(ctx context.Context) string {
	if loc := strings.TrimSpace(craigslistProfileLocation(ctx)); loc != "" {
		return loc
	}
	return strings.TrimSpace(engine.Cfg.CraigslistDefaultLocation)
}

// --- Main search function ---

// SearchCraigslistJobs searches Craigslist job listings via an HTML-first
// transport ladder (stealth static HTML → ox-browser /fetch → RSS), with
// a discovery fallback (go-search/SearXNG) as a last resort.
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

	// Craigslist is region-scoped: an empty location has nowhere to go. When
	// the caller supplied none, resolve one from the operator's profile then
	// the config default before entering the ladder. An explicit location is
	// never overridden — silently searching the wrong city is worse than
	// failing, so the fallback fires ONLY on empty input. If both fallback
	// sources are empty, location stays "" and the ladder's resolveRegion
	// guard returns errCraigslistUnmapped (the current behaviour).
	if strings.TrimSpace(location) == "" {
		if resolved := resolveCraigslistDefaultLocation(ctx); resolved != "" {
			location = resolved
		}
	}

	// HTML-first ladder (static → rendered → RSS).
	results, err := fetchCraigslistListings(ctx, query, location, limit)
	if err == nil {
		if len(results) > 0 {
			slog.Debug("craigslist: search complete", slog.Int("results", len(results)))
			return results, nil
		}
		// Genuine empty: a tier fetched OK and Craigslist has no matching listings.
		slog.Debug("craigslist: no listings",
			slog.String("query", query),
			slog.String("location", location))
		return nil, nil
	}

	// All direct tiers failed (blocked or errored). Try discovery as a last
	// resort. Discovery results are URL-shape-only (no region, freshness or
	// liveness check) and Craigslist posts expire in 30-45 days while a search
	// index does not — so a blocked connector can serve dead links. This tier
	// MUST be observable and MUST NOT convert a blocked state into a silent
	// success (3d).
	blocked := errors.Is(err, errCraigslistBlocked)
	slog.Warn("craigslist: direct tiers failed, trying discovery fallback",
		slog.String("query", query),
		slog.String("location", location),
		slog.Bool("blocked", blocked),
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
		r.Content = "**Source:** Craigslist (discovery fallback)\n\n" + r.Content
		r.Score = 0.7
		if r.Metadata == nil {
			r.Metadata = map[string]string{}
		}
		r.Metadata["source"] = "craigslist-discovery"
		discResults = append(discResults, r)
	}
	if len(discResults) > limit {
		discResults = discResults[:limit]
	}

	if len(discResults) > 0 {
		// Discovery found results — log prominently so operators can distinguish
		// discovery-sourced results from a real fetch. If the direct tiers were
		// blocked, this is masking a block with potentially-stale discovery URLs.
		// H2: increment a LABELLED counter so the laundering is observable —
		// PlatformOutcome(len>0, nil) reports outcome=ok, but this counter
		// contradicts it and an alert on reason="blocked" fires. A WARN log
		// alone satisfied neither the stated invariant nor any deployed alert.
		reason := "failed"
		if blocked {
			reason = "blocked"
		}
		if errors.Is(err, errCraigslistUnmapped) {
			reason = "unmapped"
		}
		engine.IncrCraigslistDiscoveryFallback(reason)
		slog.Warn("craigslist: discovery fallback serving results (direct tiers blocked/failed)",
			slog.String("query", query),
			slog.String("location", location),
			slog.Bool("direct_blocked", blocked),
			slog.String("reason", reason),
			slog.Int("raw", len(discovered)),
			slog.Int("listings", len(discResults)))
		return discResults, nil
	}

	// All tiers exhausted: direct failed AND discovery found nothing.
	// Return the direct-tier error (errCraigslistBlocked or a wrapped tier
	// error) — NOT (nil, nil), which is the defect that shipped as
	// outcome=empty with error=0.
	slog.Warn("craigslist: all tiers exhausted (direct failed, discovery empty)",
		slog.String("query", query),
		slog.String("location", location),
		slog.Int("discovered_raw", len(discovered)),
		slog.Any("direct_error", err))
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
