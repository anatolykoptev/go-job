package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	linkedin "github.com/anatolykoptev/go-linkedin"
	"github.com/anatolykoptev/go-kit/breaker"
	"github.com/anatolykoptev/go-kit/ratelimit"
	stealth "github.com/anatolykoptev/go-stealth"
	"github.com/anatolykoptev/go_job/internal/engine"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"golang.org/x/net/html"
)

// LinkedIn Guest API endpoint — returns HTML, no auth required.
const linkedInGuestAPI = "https://www.linkedin.com/jobs-guest/jobs/api/seeMoreJobPostings/search"

// linkedInGuestMaxStart is the hard ceiling of the LinkedIn Guest API
// (jobs-guest/jobs/api/seeMoreJobPostings/search): ~1000 results / 40 pages of
// 25. Requests past this offset yield no new results and only draw rate-limiting.
const linkedInGuestMaxStart = 1000

// linkedInPageJitter is the jittered backoff applied between Guest-API pages
// (before each fetch except the first). Reuses go-stealth.Jitter for a
// context-cancellable random sleep. Overridable in tests via withPageJitter.
//
//nolint:gochecknoglobals // package-level config, overridable in tests
var linkedInPageJitter = stealth.Jitter{
	Min: 200 * time.Millisecond,
	Max: 500 * time.Millisecond,
}

// linkedinLimiter caps concurrent LinkedIn Guest API calls independently of ATS.
// LinkedIn Voyager is far more aggressive about rate-limiting parallel requests.
//
//nolint:gochecknoglobals // package-level limiter, init-once, never mutated
var linkedinLimiter = ratelimit.NewConcurrencyLimiter(2)

// linkedinBreaker protects against LinkedIn 429/503 storms.
// After 3 consecutive failures, blocks for 60s with 2× exponential backoff.
//
//nolint:gochecknoglobals // package-level breaker, init-once, never mutated
var linkedinBreaker = breaker.New(breaker.Options{
	Name:              sourceLinkedIn,
	FailThreshold:     3,
	OpenDuration:      60 * time.Second,
	BackoffMultiplier: 2.0,
	MaxOpenDuration:   10 * time.Minute,
})

// experienceMap maps human-readable experience levels to LinkedIn filter codes.
var experienceMap = map[string]string{
	"internship": "1",
	"entry":      "2",
	"associate":  "3",
	"mid-senior": "4",
	"director":   "5",
	"executive":  "6",
}

// jobTypeMap maps human-readable job types to LinkedIn filter codes.
var jobTypeMap = map[string]string{
	"full-time":  "F",
	"part-time":  "P",
	"contract":   "C",
	"temporary":  "T",
	"internship": "I",
	"volunteer":  "V",
}

// remoteMap maps remote/onsite to LinkedIn workplace type codes.
var remoteMap = map[string]string{
	"onsite": "1",
	"hybrid": "2",
	jobTypeRemote: "3",
}

// timeRangeMap maps human-readable time ranges to LinkedIn seconds-based codes.
var timeRangeMap = map[string]string{
	"day":   "r86400",
	"week":  "r604800",
	"month": "r2592000",
}

// LinkedInJob represents a parsed job card from the Guest API.
type LinkedInJob struct {
	Title    string `json:"title"`
	Company  string `json:"company"`
	Location string `json:"location"`
	URL      string `json:"url"`
	JobID    string `json:"job_id"`
	Posted   string `json:"posted"`
}

// jobIDRe extracts job ID from LinkedIn job URLs.
// Matches both /jobs/view/4335742219 and /jobs/view/golang-developer-at-ceipal-4335742219
var jobIDRe = regexp.MustCompile(`/jobs/view/[^?]*?(\d{7,})`)

// ExtractJobID extracts LinkedIn job ID from a URL.
func ExtractJobID(jobURL string) string {
	if m := jobIDRe.FindStringSubmatch(jobURL); m != nil {
		return m[1]
	}
	return ""
}

// salaryMap maps human-readable salary thresholds to LinkedIn f_SB2 filter codes.
var salaryMap = map[string]string{
	"40k+":  "1",
	"60k+":  "2",
	"80k+":  "3",
	"100k+": "4",
	"120k+": "5",
	"140k+": "6",
	"160k+": "7",
	"180k+": "8",
	"200k+": "9",
}

// linkedInGeoIDs maps common location strings (lowercase) to LinkedIn geoId values.
// Using geoId provides more precise geographic filtering than text-based location.
var linkedInGeoIDs = map[string]string{
	"united states":  "103644278",
	"us":             "103644278",
	"usa":            "103644278",
	"united kingdom": "101165590",
	"uk":             "101165590",
	"great britain":  "101165590",
	"germany":        "101282230",
	"canada":         "101174742",
	"france":         "105015875",
	"netherlands":    "102890719",
	"poland":         "105072130",
	"india":          "102713980",
	"australia":      "101452733",
	"singapore":      "102454443",
	"spain":          "105646813",
	"sweden":         "105117694",
	"switzerland":    "106693272",
	"denmark":        "104514075",
	"norway":         "103819153",
	"finland":        "100456013",
	"israel":         "101620260",
	"brazil":         "106057199",
	"ukraine":        "102264497",
	"portugal":       "100364837",
	"ireland":        "104738515",
	"austria":        "103883259",
	"czech republic": "104508036",
	"czechia":        "104508036",
	"romania":        "106670623",
	"hungary":        "100288700",
	"new york":       "105080838",
	"san francisco":  "90000084",
	"london":         "90009496",
	"berlin":         "103035651",
	"amsterdam":      "102011674",
	"toronto":        "100025096",
	"melbourne":      "105088671",
	"sydney":         "104769905",
	"bangalore":      "105214831",
	"tel aviv":       "101822562",
	jobTypeRemote:         "91000001",
}

// SearchLinkedInJobs queries the LinkedIn Guest API and returns parsed job cards.
// maxResults controls how many jobs to fetch (rounds up to nearest 25). 0 means 25.
// easyApply=true filters to Easy Apply jobs only (f_JIYN=true param).
func SearchLinkedInJobs(ctx context.Context, query, location, experience, jobType, remote, timeRange, salary string, maxResults int, easyApply bool) ([]LinkedInJob, error) {
	if maxResults <= 0 {
		maxResults = 25
	}

	u, err := url.Parse(linkedInGuestAPI)
	if err != nil {
		return nil, err
	}

	// Build base query params (filters, no start offset yet).
	baseQ := u.Query()
	baseQ.Set("keywords", query)
	baseQ.Set("sortBy", "DD") // sort by date
	if location != "" {
		baseQ.Set("location", location)
		// Add geoId for precise geographic filtering when location is known.
		if geoID, ok := linkedInGeoIDs[strings.ToLower(strings.TrimSpace(location))]; ok {
			baseQ.Set("geoId", geoID)
		}
	}
	if v, ok := experienceMap[strings.ToLower(experience)]; ok {
		baseQ.Set("f_E", v)
	}
	if v, ok := jobTypeMap[strings.ToLower(jobType)]; ok {
		baseQ.Set("f_JT", v)
	}
	if v, ok := remoteMap[strings.ToLower(remote)]; ok {
		baseQ.Set("f_WT", v)
	}
	if v, ok := timeRangeMap[strings.ToLower(timeRange)]; ok {
		baseQ.Set("f_TPR", v)
	}
	if v, ok := salaryMap[strings.ToLower(strings.TrimSpace(salary))]; ok {
		baseQ.Set("f_SB2", v)
	}
	if easyApply {
		baseQ.Set("f_JIYN", "true")
	}

	// Paginate in steps of 25 until we have enough results, LinkedIn returns
	// empty, or we hit the Guest-API hard ceiling (~1000 results / 40 pages).
	// A jittered backoff is applied between pages (not before the first, not
	// after the last) to avoid burst-fetching and draw less rate-limiting.
	var allJobs []LinkedInJob
	for start := 0; len(allJobs) < maxResults && start < linkedInGuestMaxStart; start += 25 {
		// Jittered backoff before every fetch except the first. The sleep is
		// context-cancellable so a cancelled search aborts promptly instead of
		// waiting out the full jitter. On ctx cancellation we return what we
		// have so far (consistent with linkedInRequest's ctx-aware behaviour).
		if start > 0 {
			if err := linkedInPageJitter.Sleep(ctx); err != nil {
				break
			}
		}

		q := baseQ
		q.Set("start", strconv.Itoa(start))
		u.RawQuery = q.Encode()

		body, err := linkedInRequest(ctx, u.String())
		if err != nil {
			if start == 0 {
				return nil, err
			}
			// Already have some results from earlier pages.
			break
		}

		page := parseLinkedInHTML(string(body))
		if len(page) == 0 {
			break // No more results.
		}
		allJobs = append(allJobs, page...)

		if len(page) < 25 {
			break // Last page (partial).
		}
	}

	if len(allJobs) > maxResults {
		allJobs = allJobs[:maxResults]
	}
	return allJobs, nil
}

// linkedInTierFunc is the uniform signature for all three cascade tiers.
// Each tier returns (status, body, err); the cascade classifier inspects all three.
type linkedInTierFunc func(ctx context.Context, targetURL string, headers map[string]string) (status int, body []byte, err error)

// linkedInTier1Fetch is the Tier-1 stealth fetch via go-stealth BrowserClient
// (Chrome TLS fingerprint), with net/http fallback when BrowserClient is nil.
// Overridable in tests.
var linkedInTier1Fetch linkedInTierFunc = linkedInTier1Stealth

// linkedInTier2Fetch is the Tier-2 proxy fetch via engine.FetchProxyBody
// (proxy + ox-browser /fetch-smart anti-bot fallback). Overridable in tests.
var linkedInTier2Fetch linkedInTierFunc = linkedInTier2Proxy

// linkedInTier3Fetch is the Tier-3 headless render via go-wowa Playwright/Chrome.
// Overridable in tests.
var linkedInTier3Fetch linkedInTierFunc = linkedInTier3Render

// linkedInRequest fetches a LinkedIn URL through a three-tier fallback cascade:
//
//  1. Tier 1: go-stealth BrowserClient (Chrome JA3) or net/http fallback.
//  2. Tier 2: engine.FetchProxyBody (proxy pool + ox-browser /fetch-smart).
//  3. Tier 3: fetchRenderedHTML (go-wowa headless Chrome render).
//
// Each response is classified via classifyLinkedInResponse. The first tier that
// returns liOK short-circuits the cascade. A breaker failure is recorded only
// after the final tier also returns non-OK; a breaker success is recorded when
// any tier yields liOK. The linkedinLimiter and FetchTimeout context wrap the
// entire cascade.
//
// Each escalation emits a slog.Warn with {tier, status, kind, err} so operators
// can distinguish 429-throttle-everywhere from hard-block from network-down.
// On total failure the returned error wraps errLinkedInCascadeExhausted and is
// enriched with the LAST tier's classified kind + status for downstream
// alerting (issue #291).
func linkedInRequest(ctx context.Context, targetURL string) ([]byte, error) {
	if !linkedinBreaker.Allow() {
		return nil, fmt.Errorf("linkedin breaker open: %w", breaker.ErrOpen)
	}

	release, err := linkedinLimiter.Acquire(ctx)
	if err != nil {
		linkedinBreaker.Record(false)
		return nil, err
	}
	defer release()

	ctx, cancel := context.WithTimeout(ctx, engine.Cfg.FetchTimeout)
	defer cancel()

	headers := engine.ChromeHeaders()
	headers["accept"] = "text/html,application/xhtml+xml,application/xml;q=0.9"
	headers["referer"] = "https://www.linkedin.com/"

	// tierResult captures a tier's classified outcome for escalation logging
	// and the final enriched error.
	type tierResult struct {
		tier   int
		status int
		kind   linkedInBlockKind
		err    error
	}
	classify := func(tier int, status int, body []byte, err error) (tierResult, bool) {
		// A transport error (no HTTP response) is a cascade-level network error,
		// not a LinkedIn block — distinct kind for alerting.
		kind := liNetworkError
		if err == nil {
			kind = classifyLinkedInResponse(status, body)
		}
		ok := err == nil && kind == liOK
		return tierResult{tier: tier, status: status, kind: kind, err: err}, ok
	}
	var last tierResult

	// Tier 1: stealth BrowserClient (or net/http fallback).
	status, body, ferr := linkedInTier1Fetch(ctx, targetURL, headers)
	if r, ok := classify(1, status, body, ferr); ok {
		linkedinBreaker.Record(true)
		return body, nil
	} else {
		last = r
		slog.Warn("linkedin cascade: escalating from tier",
			slog.Int("tier", r.tier),
			slog.Int("status", r.status),
			slog.String("kind", r.kind.String()),
			slog.Any("err", r.err),
		)
	}

	// Tier 2: proxy + ox-browser /fetch-smart.
	status, body, ferr = linkedInTier2Fetch(ctx, targetURL, headers)
	if r, ok := classify(2, status, body, ferr); ok {
		linkedinBreaker.Record(true)
		return body, nil
	} else {
		last = r
		slog.Warn("linkedin cascade: escalating from tier",
			slog.Int("tier", r.tier),
			slog.Int("status", r.status),
			slog.String("kind", r.kind.String()),
			slog.Any("err", r.err),
		)
	}

	// Tier 3: go-wowa headless render.
	status, body, ferr = linkedInTier3Fetch(ctx, targetURL, headers)
	if r, ok := classify(3, status, body, ferr); ok {
		linkedinBreaker.Record(true)
		return body, nil
	} else {
		last = r
		slog.Warn("linkedin cascade: escalating from tier",
			slog.Int("tier", r.tier),
			slog.Int("status", r.status),
			slog.String("kind", r.kind.String()),
			slog.Any("err", r.err),
		)
	}

	linkedinBreaker.Record(false)
	return nil, fmt.Errorf("linkedin cascade exhausted (last tier=%d status=%d kind=%s): %w",
		last.tier, last.status, last.kind, errLinkedInCascadeExhausted)
}

// linkedInTier1Stealth is the Tier-1 fetch: go-stealth BrowserClient (Chrome
// TLS fingerprint) when available, falling back to standard net/http client.
// LinkedIn blocks non-browser TLS fingerprints, so BrowserClient is preferred.
func linkedInTier1Stealth(ctx context.Context, targetURL string, headers map[string]string) (int, []byte, error) {
	if engine.Cfg.BrowserClient != nil {
		data, _, status, err := engine.Cfg.BrowserClient.Do("GET", targetURL, headers, nil)
		if err != nil {
			return 0, nil, err
		}
		return status, data, nil
	}

	// Fallback: standard HTTP client (when BrowserClient is nil).
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("User-Agent", engine.UserAgentChrome)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	resp, err := engine.Cfg.HTTPClient.Do(req) //nolint:gosec // intentional outbound HTTP request
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, rerr := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	return resp.StatusCode, data, rerr
}

// linkedInTier2Proxy is the Tier-2 fetch: engine.FetchProxyBody routes through
// the go-engine Fetcher, which has the ox-browser /fetch-smart anti-bot
// fallback baked in (wired in internal/engine/config.go via fetch.WithOxBrowser
// when OX_BROWSER_URL is set).
func linkedInTier2Proxy(ctx context.Context, targetURL string, headers map[string]string) (int, []byte, error) {
	body, err := engine.FetchProxyBody(ctx, targetURL, headers)
	if err != nil {
		return 0, nil, err
	}
	return 200, body, nil
}

// linkedInTier3Render is the Tier-3 fetch: go-wowa headless Playwright/Chrome
// render via fetchRenderedHTML. Headers are accepted but ignored (go-wowa uses
// its own browser profile).
func linkedInTier3Render(ctx context.Context, targetURL string, _ map[string]string) (int, []byte, error) {
	html, err := fetchRenderedHTML(ctx, targetURL)
	if err != nil {
		return 0, nil, err
	}
	return 200, []byte(html), nil
}

// parseLinkedInHTML extracts job cards from the Guest API HTML response
// using golang.org/x/net/html for robust tree-based parsing.
func parseLinkedInHTML(body string) []LinkedInJob {
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return nil
	}

	var jobs []LinkedInJob
	for _, li := range findElements(doc, "li") {
		if job := parseJobCard(li); job.Title != "" && job.URL != "" {
			jobs = append(jobs, job)
		}
	}
	return jobs
}

// parseJobCard extracts a LinkedInJob from an <li> node.
func parseJobCard(li *html.Node) LinkedInJob {
	var job LinkedInJob

	// Extract job URL from "base-card__full-link" link
	if link := findByClass(li, "base-card__full-link"); link != nil {
		if href := getAttr(link, "href"); href != "" {
			job.URL = strings.TrimSpace(strings.SplitN(href, "?", 2)[0])
			job.JobID = ExtractJobID(job.URL)
		}
	}

	// Extract title from "base-search-card__title"
	if n := findByClass(li, "base-search-card__title"); n != nil {
		job.Title = strings.TrimSpace(textContent(n))
	}

	// Extract company from "base-search-card__subtitle"
	if n := findByClass(li, "base-search-card__subtitle"); n != nil {
		job.Company = strings.TrimSpace(textContent(n))
	}

	// Extract location from "job-search-card__location"
	if n := findByClass(li, "job-search-card__location"); n != nil {
		job.Location = strings.TrimSpace(textContent(n))
	}

	// Extract time posted — prefer ISO datetime attribute over relative text
	if n := findByClass(li, "job-search-card__listdate"); n != nil {
		if dt := getAttr(n, "datetime"); dt != "" {
			job.Posted = strings.TrimSpace(dt)
		} else {
			job.Posted = strings.TrimSpace(textContent(n))
		}
	}

	return job
}

// --- HTML tree helpers ---

// getAttr returns the value of an attribute on a node, or "".
func getAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// hasClass checks if a node's class attribute contains the given class name.
func hasClass(n *html.Node, className string) bool {
	return strings.Contains(getAttr(n, "class"), className)
}

// textContent recursively extracts all text from a node.
func textContent(n *html.Node) string {
	if n.Type == html.TextNode {
		return n.Data
	}
	var sb strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		sb.WriteString(textContent(c))
	}
	return sb.String()
}

// findByClass finds the first descendant element with the given class.
func findByClass(n *html.Node, className string) *html.Node {
	if n.Type == html.ElementNode && hasClass(n, className) {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findByClass(c, className); found != nil {
			return found
		}
	}
	return nil
}

// findElements finds all descendant elements with the given tag name.
func findElements(n *html.Node, tag string) []*html.Node {
	var results []*html.Node
	if n.Type == html.ElementNode && n.Data == tag {
		results = append(results, n)
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		results = append(results, findElements(c, tag)...)
	}
	return results
}

// FetchJobDetails fetches a single LinkedIn job page and extracts structured data
// from the JSON-LD schema.org/JobPosting block.
func FetchJobDetails(ctx context.Context, jobURL string) (string, error) {
	// Check cache first
	if cached, ok := engine.CacheGetJobDetails(ctx, jobURL); ok {
		return cached, nil
	}

	details, err := fetchJobDetailsUncached(ctx, jobURL)
	if err != nil {
		return "", err
	}

	engine.CacheSetJobDetails(ctx, jobURL, details)
	return details, nil
}

// fetchJobDetailsUncached fetches a single LinkedIn job page and extracts structured data.
func fetchJobDetailsUncached(ctx context.Context, jobURL string) (string, error) {
	bodyBytes, err := linkedInRequest(ctx, jobURL)
	if err != nil {
		return "", err
	}

	html := string(bodyBytes)

	// Try to extract JSON-LD structured data
	if jsonLD := extractJSONLD(html); jsonLD != "" {
		return jsonLD, nil
	}

	// Fallback: extract description section via html-to-markdown
	if descHTML := extractJobDescription(html); descHTML != "" {
		md, err := htmltomarkdown.ConvertString(descHTML)
		if err == nil && md != "" {
			return md, nil
		}
	}

	return "", errors.New("no job details found")
}

// extractJSONLD extracts and formats the schema.org/JobPosting JSON-LD block.
func extractJSONLD(html string) string {
	marker := `"@type":"JobPosting"`
	markerAlt := `"@type": "JobPosting"`

	idx := strings.Index(html, marker)
	if idx == -1 {
		idx = strings.Index(html, markerAlt)
	}
	if idx == -1 {
		return ""
	}

	// Find the enclosing <script> tag
	scriptStart := strings.LastIndex(html[:idx], "<script")
	if scriptStart == -1 {
		return ""
	}
	scriptEnd := strings.Index(html[scriptStart:], "</script>")
	if scriptEnd == -1 {
		return ""
	}

	scriptContent := html[scriptStart : scriptStart+scriptEnd]
	// Extract JSON content between > and </script>
	jsonStart := strings.Index(scriptContent, ">")
	if jsonStart == -1 {
		return ""
	}
	jsonStr := strings.TrimSpace(scriptContent[jsonStart+1:])

	// Parse and extract key fields
	var data map[string]interface{}
	if err := json.Unmarshal([]byte(jsonStr), &data); err != nil {
		return ""
	}

	var parts []string

	if title, ok := data["title"].(string); ok {
		parts = append(parts, "**Title:** "+title)
	}
	if desc, ok := data["description"].(string); ok {
		// Convert HTML description to markdown
		md, err := htmltomarkdown.ConvertString(desc)
		if err == nil {
			desc = md
		}
		desc = engine.TruncateRunes(desc, 3000, "...")
		parts = append(parts, "**Description:**\n"+desc)
	}
	if org, ok := data["hiringOrganization"].(map[string]interface{}); ok {
		if name, ok := org["name"].(string); ok {
			parts = append(parts, "**Company:** "+name)
		}
	}
	if loc, ok := data["jobLocation"].(map[string]interface{}); ok {
		if addr, ok := loc["address"].(map[string]interface{}); ok {
			locParts := []string{}
			if city, ok := addr["addressLocality"].(string); ok {
				locParts = append(locParts, city)
			}
			if country, ok := addr["addressCountry"].(string); ok {
				locParts = append(locParts, country)
			}
			if len(locParts) > 0 {
				parts = append(parts, "**Location:** "+strings.Join(locParts, ", "))
			}
		}
	}
	if empType, ok := data["employmentType"].(string); ok {
		parts = append(parts, "**Type:** "+empType)
	}
	if salary, ok := data["baseSalary"].(map[string]interface{}); ok {
		if val, ok := salary["value"].(map[string]interface{}); ok {
			min, _ := val["minValue"].(float64)
			max, _ := val["maxValue"].(float64)
			currency, _ := salary["currency"].(string)
			if min > 0 || max > 0 {
				parts = append(parts, fmt.Sprintf("**Salary:** %.0f-%.0f %s", min, max, currency))
			}
		}
	}

	return strings.Join(parts, "\n\n")
}

// extractJobDescription extracts the job description HTML section using tree parsing.
func extractJobDescription(body string) string {
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return ""
	}
	classes := []string{
		"show-more-less-html__markup",
		"description__text",
		"job-description",
	}
	for _, cls := range classes {
		if n := findByClass(doc, cls); n != nil {
			return renderChildren(n)
		}
	}
	return ""
}

// renderChildren returns the inner HTML of a node as a string.
func renderChildren(n *html.Node) string {
	var sb strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		_ = html.Render(&sb, c)
	}
	return sb.String()
}

// engine.LinkedInJobsToSearxngResults converts LinkedIn jobs to engine.SearxngResult format
// for pipeline compatibility. Fetches details for top N jobs in parallel
// with staggered delays to avoid rate limiting.
func LinkedInJobsToSearxngResults(ctx context.Context, jobs []LinkedInJob, fetchDetailCount int) []engine.SearxngResult {
	// Build base snippets for all jobs
	snippets := make([]string, len(jobs))
	for i, job := range jobs {
		s := job.Company
		if job.Location != "" {
			s += " | " + job.Location
		}
		if job.Posted != "" {
			s += " | Posted: " + job.Posted
		}
		snippets[i] = s
	}

	// Fetch details in parallel with staggered delays
	type detailResult struct {
		idx     int
		content string
	}
	detailCh := make(chan detailResult, fetchDetailCount)
	for i := 0; i < fetchDetailCount && i < len(jobs); i++ {
		go func(idx int, jobURL string) {
			if idx > 0 {
				select {
				case <-time.After(time.Duration(idx) * time.Second):
				case <-ctx.Done():
					detailCh <- detailResult{idx, ""}
					return
				}
			}
			details, err := FetchJobDetails(ctx, jobURL)
			if err != nil {
				slog.Debug("linkedin: failed to fetch job details", slog.String("url", jobURL), slog.Any("error", err))
				detailCh <- detailResult{idx, ""}
				return
			}
			detailCh <- detailResult{idx, details}
		}(i, jobs[i].URL)
	}

	// Collect results
	fetched := min(fetchDetailCount, len(jobs))
	for range fetched {
		r := <-detailCh
		if r.content != "" {
			snippets[r.idx] = r.content
		}
	}

	// Build results
	results := make([]engine.SearxngResult, len(jobs))
	for i, job := range jobs {
		results[i] = engine.SearxngResult{
			Title:   job.Title + " at " + job.Company,
			Content: snippets[i],
			URL:     job.URL,
			Score:   0,
		}
	}
	return results
}

// --- Voyager API wrappers (go-linkedin client) ---

// VoyagerProfile fetches a full LinkedIn profile via Voyager API (authenticated).
func VoyagerProfile(ctx context.Context, handle string) (*linkedin.Profile, error) {
	return withRetry(ctx, func(c *linkedin.Client) (*linkedin.Profile, error) {
		return c.GetProfile(ctx, handle)
	})
}

// VoyagerCompany fetches a LinkedIn company page via Voyager API.
func VoyagerCompany(ctx context.Context, slug string) (*linkedin.Company, error) {
	return withRetry(ctx, func(c *linkedin.Client) (*linkedin.Company, error) {
		return c.GetCompany(ctx, slug)
	})
}

// VoyagerJobs searches LinkedIn job listings via Voyager API.
func VoyagerJobs(ctx context.Context, params linkedin.JobSearchParams) ([]linkedin.Job, error) {
	return withRetry(ctx, func(c *linkedin.Client) ([]linkedin.Job, error) {
		return c.SearchJobs(ctx, params)
	})
}

// VoyagerSearchPeople searches LinkedIn for people matching the query.
func VoyagerSearchPeople(ctx context.Context, query string, limit int) ([]linkedin.SearchResult, error) {
	return withRetry(ctx, func(c *linkedin.Client) ([]linkedin.SearchResult, error) {
		return c.SearchPeople(ctx, linkedin.SearchParams{Query: query, Limit: limit})
	})
}

// VoyagerSearchCompanies searches LinkedIn for companies matching the query.
func VoyagerSearchCompanies(ctx context.Context, query string, limit int) ([]linkedin.SearchResult, error) {
	return withRetry(ctx, func(c *linkedin.Client) ([]linkedin.SearchResult, error) {
		return c.SearchCompanies(ctx, linkedin.SearchParams{Query: query, Limit: limit})
	})
}

// VoyagerPosts fetches recent posts for a LinkedIn profile.
func VoyagerPosts(ctx context.Context, handle string, limit int) ([]linkedin.Post, error) {
	return withRetry(ctx, func(c *linkedin.Client) ([]linkedin.Post, error) {
		profile, err := c.GetProfile(ctx, handle)
		if err != nil {
			return nil, err
		}
		profileID := linkedin.ExtractProfileID(profile.URN)
		return c.GetPosts(ctx, profileID, limit)
	})
}

// VoyagerRating computes influence and quality metrics for a LinkedIn profile.
func VoyagerRating(ctx context.Context, handle string) (*linkedin.ProfileRating, error) {
	return withRetry(ctx, func(c *linkedin.Client) (*linkedin.ProfileRating, error) {
		profile, err := c.GetProfile(ctx, handle)
		if err != nil {
			return nil, err
		}
		profileID := linkedin.ExtractProfileID(profile.URN)
		posts, _ := c.GetPosts(ctx, profileID, 20)
		return linkedin.ComputeRating(profile, posts), nil
	})
}
