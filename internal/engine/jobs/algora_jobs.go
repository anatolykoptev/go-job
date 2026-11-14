package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/anatolykoptev/go-kit/breaker"
	"github.com/anatolykoptev/go_job/internal/engine"
	"golang.org/x/net/html"
)

// algoraJobRe matches single-job URLs: algora.io/<org>/job/<id>.
var algoraJobRe = regexp.MustCompile(`algora\.io/([^/?#]+)/job/([^/?#]+)`)

// algoraJobSource is the source label for Algora job postings in hunt_jobs.
const algoraJobSource = "algora-jobs"


// algoraRemoteRow is the row key for remote/work-type field in Tier-2 row-walk.
const algoraRemoteRow = "remote"
// algoraJobsBreaker is a per-source circuit breaker for Algora jobs (mirrors ats.go breakers).
//
//nolint:gochecknoglobals // package-level breaker, init-once, never mutated
var algoraJobsBreaker = breaker.New(breaker.Options{
	Name:          "algora_jobs",
	FailThreshold: 3,
	OpenDuration:  30 * time.Second,
})

// errAlgoraJobGone is returned when an Algora job URL redirects (job deleted/closed)
// or returns a non-200 status. It signals the job is no longer active.
var errAlgoraJobGone = errors.New("algora: job gone or redirected")

// parseAlgoraJobURL extracts (org, jobID) from a single-job URL.
// Returns ("","",false) for non-job URLs (boards, bounties, etc.).
func parseAlgoraJobURL(rawURL string) (org, jobID string, ok bool) {
	m := algoraJobRe.FindStringSubmatch(rawURL)
	if m == nil {
		return "", "", false
	}
	// Strip any trailing slash from jobID.
	id := strings.TrimRight(m[2], "/")
	if id == "" {
		return "", "", false
	}
	return m[1], id, true
}

// FetchAlgoraJob fetches and parses a single Algora job page into a JobListing.
// Cached 15m by job id; best-effort — returns (nil, err) on fetch/parse failure.
func FetchAlgoraJob(ctx context.Context, jobURL string) (*engine.JobListing, error) {
	_, jobID, ok := parseAlgoraJobURL(jobURL)
	if !ok {
		return nil, fmt.Errorf("algora-jobs: not a single-job URL: %s", jobURL)
	}
	// Defense-in-depth: assert host is algora.io regardless of what the regex matched.
	if parsed, parseErr := url.Parse(jobURL); parseErr != nil || parsed.Hostname() != "algora.io" {
		return nil, fmt.Errorf("algora-jobs: URL host must be algora.io: %s", jobURL)
	}

	cacheKey := "algora_job_" + jobID
	if cached, hit := engine.CacheLoadJSON[engine.JobListing](ctx, cacheKey); hit {
		slog.Debug("algora-jobs: cache hit", slog.String("job_id", jobID))
		return &cached, nil
	}

	engine.IncrAlgoraJobsRequests()

	fetchCtx, cancel := context.WithTimeout(ctx, engine.Cfg.FetchTimeout)
	defer cancel()

	if !algoraJobsBreaker.Allow() {
		return nil, fmt.Errorf("algora-jobs: breaker open: %w", breaker.ErrOpen)
	}

	body, fetchErr := fetchAlgoraJobRaw(fetchCtx, engine.Cfg.HTTPClient, jobURL)
	// For gone/redirect: server was reachable (not a transport failure) → record as success to avoid tripping breaker.
	if errors.Is(fetchErr, errAlgoraJobGone) {
		algoraJobsBreaker.Record(true)
		return nil, errAlgoraJobGone
	}
	algoraJobsBreaker.Record(fetchErr == nil)
	if fetchErr != nil {
		return nil, fmt.Errorf("algora-jobs: fetch %s: %w", jobURL, fetchErr)
	}

	listing, err := parseAlgoraJob(body, jobURL)
	if err != nil {
		return nil, fmt.Errorf("algora-jobs: parse %s: %w", jobURL, err)
	}

	engine.CacheStoreJSON(ctx, cacheKey, "", *listing)
	return listing, nil
}

// fetchAlgoraJobRaw performs a single no-redirect HTTP GET and returns the response body.
// On 3xx → returns ("", errAlgoraJobGone).
// On non-200 → returns ("", errAlgoraJobGone).
// On 200 → returns (body, nil).
func fetchAlgoraJobRaw(ctx context.Context, client *http.Client, jobURL string) (string, error) {
	noRedirectClient := &http.Client{
		Transport: client.Transport,
		Timeout:   client.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jobURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", engine.UserAgentChrome)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	resp, err := noRedirectClient.Do(req) //nolint:gosec // intentional outbound HTTP
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", errAlgoraJobGone
	}
	raw, err := readLimitedBody(resp.Body, 2*1024*1024)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// DiscoverAlgoraOrgJobs scrapes algora.io/<org>/jobs for ACTIVE job links and
// fetches+parses each. Shared atsLimiter caps the fan-out burst.
func DiscoverAlgoraOrgJobs(ctx context.Context, org string) ([]engine.JobListing, error) {
	boardURL := "https://algora.io/" + org + "/jobs"

	engine.IncrAlgoraJobsRequests()

	fetchCtx, cancel := context.WithTimeout(ctx, engine.Cfg.FetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, boardURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", engine.UserAgentChrome)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	resp, err := engine.RetryHTTP(fetchCtx, engine.DefaultRetryConfig, func() (*http.Response, error) {
		return engine.Cfg.HTTPClient.Do(req) //nolint:gosec // intentional outbound HTTP request
	})
	if err != nil {
		return nil, fmt.Errorf("algora-jobs: board fetch %s: %w", boardURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("algora-jobs: board status %d for %s", resp.StatusCode, boardURL)
	}

	raw, err := readLimitedBody(resp.Body, 4*1024*1024)
	if err != nil {
		return nil, err
	}

	// Extract all single-job links from the board page.
	jobURLs := extractAlgoraJobLinks(string(raw), org)
	slog.Debug("algora-jobs: discovered job links", slog.String("org", org), slog.Int("count", len(jobURLs)))

	if len(jobURLs) == 0 {
		return nil, nil
	}

	// Fan-out: fetch each job page under a local semaphore (mirrors atsLimiter pattern).
	type result struct {
		listing *engine.JobListing
		err     error
	}
	results := make([]result, len(jobURLs))
	maxConcurrent := getATSMaxConcurrent()
	sem := make(chan struct{}, maxConcurrent)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i, u := range jobURLs {
			sem <- struct{}{}
			go func(idx int, jobURL string) {
				defer func() { <-sem }()
				listing, fetchErr := FetchAlgoraJob(ctx, jobURL)
				results[idx] = result{listing: listing, err: fetchErr}
			}(i, u)
		}
		// Drain semaphore to wait for all goroutines.
		for range maxConcurrent {
			sem <- struct{}{}
		}
	}()
	<-done

	var listings []engine.JobListing
	for _, r := range results {
		if r.err != nil {
			if errors.Is(r.err, errAlgoraJobGone) {
				slog.Debug("algora-jobs: job gone or redirected", slog.Any("error", r.err))
			} else {
				slog.Warn("algora-jobs: fan-out fetch error", slog.Any("error", r.err))
			}
			continue
		}
		if r.listing != nil {
			listings = append(listings, *r.listing)
		}
	}
	return listings, nil
}

// extractAlgoraJobLinks scans the board HTML for single-job URLs matching /<org>/job/<id>.
func extractAlgoraJobLinks(body, org string) []string {
	// Use regex to extract href values matching the job pattern.
	linkRe := regexp.MustCompile(`href="(/` + regexp.QuoteMeta(org) + `/job/[^"/?#]+)"`)
	matches := linkRe.FindAllStringSubmatch(body, -1)

	seen := make(map[string]bool)
	var urls []string
	for _, m := range matches {
		jobURL := "https://algora.io" + m[1]
		if !seen[jobURL] {
			seen[jobURL] = true
			urls = append(urls, jobURL)
		}
	}
	return urls
}

// parseAlgoraJob parses the HTML of a single algora.io/<org>/job/<id> page into a JobListing.
// Three tiers:
//   - Tier 1: og:title, og:url → title, URL, org, jobID
//   - Tier 2: key/value rows (div.flex.justify-between.border-b) → salary/location/equity
//   - Tier 3: div.prose.prose-invert → description (NEVER og:description)
func parseAlgoraJob(body, jobURL string) (*engine.JobListing, error) {
	if body == "" {
		return &engine.JobListing{
			URL:     jobURL,
			Source:  algoraJobSource,
			JobType: jobTypeJob,
		}, nil
	}

	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		// html.Parse is very lenient — errors are extremely rare; return empty listing.
		slog.Warn("algora-jobs: html.Parse error", slog.Any("error", err))
		return &engine.JobListing{
			URL:     jobURL,
			Source:  algoraJobSource,
			JobType: jobTypeJob,
		}, nil
	}

	// --- Tier 1: Open Graph meta ---
	title, ogURL := extractAlgoraOGMeta(doc)

	// Canonical URL from og:url; fall back to input jobURL.
	canonical := ogURL
	if canonical == "" {
		canonical = jobURL
	}

	// Parse org + jobID from canonical URL.
	org, jobID, _ := parseAlgoraJobURL(canonical)
	if jobID == "" {
		// Try input URL as fallback.
		org, jobID, _ = parseAlgoraJobURL(jobURL)
	}

	// --- Company name from org slug (title-case fallback) ---
	company := extractAlgoraCompanyName(doc, org)

	// --- Tier 2: row-walk key/value block ---
	rows := parseAlgoraJobRows(doc)

	listing := &engine.JobListing{
		Title:   title,
		Company: company,
		URL:     canonical,
		JobID:   jobID,
		Source:  algoraJobSource,
		JobType: jobTypeJob,
	}

	// Process known rows.
	for key, val := range rows {
		keyLower := strings.ToLower(strings.TrimSpace(key))
		val = strings.TrimSpace(val)
		switch keyLower {
		case "base salary":
			applyAlgoraSalary(listing, val)
		case "location": //nolint:goconst
			listing.Location = val
		case "equity":
			// Equity is free text — store as a tag, never as a number.
			if val != "" {
				listing.Skills = append(listing.Skills, "equity:"+val)
			} else {
				listing.Skills = append(listing.Skills, "equity")
			}
		case algoraRemoteRow: // "remote" is canonical winner
			listing.Remote = val
		case "type":
			if listing.Remote == "" {
				listing.Remote = val
			}
		}
	}

	// --- Tier 3: prose description — NEVER og:description ---
	listing.Description = extractAlgoraDescription(doc)

	return listing, nil
}

// extractAlgoraOGMeta walks the node tree and returns (og:title, og:url).
func extractAlgoraOGMeta(doc *html.Node) (title, ogURL string) {
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "meta" {
			prop := getAttr(n, "property")
			content := getAttr(n, "content")
			switch prop {
			case "og:title":
				if title == "" {
					title = strings.TrimSpace(content)
				}
			case "og:url":
				if ogURL == "" {
					ogURL = strings.TrimSpace(content)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return title, ogURL
}

// extractAlgoraCompanyName tries to find the org display name from the page.
// Strategy (in order):
//  1. First <a href="/<orgSlug>"> link text (direct org profile link).
//  2. First <span> with class "font-semibold" and "text-foreground" in the header
//     region (the org name heading used by the current Algora LiveView layout).
//  3. Fallback: title-case the URL slug ("comfy" → "Comfy").
//
// Note: the Algora LiveView layout does NOT render an <a href="/comfy"> (bare slug)
// link — it uses /comfy/jobs for navigation. The header span approach is more
// robust for the current markup (verified 2026-06-19 against the live Comfy page).
func extractAlgoraCompanyName(doc *html.Node, orgSlug string) string {
	// Strategy 1: look for <a href="/<orgSlug>"> (exact, no trailing path).
	var found string
	var walkA func(*html.Node)
	walkA = func(n *html.Node) {
		if found != "" {
			return
		}
		if n.Type == html.ElementNode && n.Data == "a" {
			href := getAttr(n, "href")
			if href == "/"+orgSlug || href == "/"+orgSlug+"/" {
				text := strings.TrimSpace(textContent(n))
				if text != "" {
					found = text
					return
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walkA(c)
		}
	}
	walkA(doc)
	if found != "" {
		return found
	}

	// Strategy 2: find the first <span> that has both "font-semibold" and
	// "text-foreground" classes (the org name heading in the Algora header).
	var walkSpan func(*html.Node)
	walkSpan = func(n *html.Node) {
		if found != "" {
			return
		}
		if n.Type == html.ElementNode && n.Data == "span" &&
			hasClass(n, "font-semibold") && hasClass(n, "text-foreground") {
			text := strings.TrimSpace(textContent(n))
			if text != "" {
				found = text
				return
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walkSpan(c)
		}
	}
	walkSpan(doc)
	if found != "" {
		return found
	}

	// Strategy 3: title-case slug ("comfy" → "Comfy", "comfy-org" → "Comfy Org").
	return titleCaseSlug(orgSlug)
}

// titleCaseSlug converts "comfy-org" or "comfy_org" to "Comfy Org".
func titleCaseSlug(slug string) string {
	slug = strings.ReplaceAll(slug, "-", " ")
	slug = strings.ReplaceAll(slug, "_", " ")
	words := strings.Fields(slug)
	for i, w := range words {
		if len(w) > 0 {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}

// parseAlgoraJobRows walks the DOM and finds all key/value rows matching:
//
//	div.flex.justify-between.border-b
//	  ├─ span.text-muted-foreground  → KEY
//	  └─ span (last child)           → VALUE (all inner text concatenated)
func parseAlgoraJobRows(doc *html.Node) map[string]string {
	rows := make(map[string]string)
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "div" &&
			hasClass(n, "flex") &&
			hasClass(n, "justify-between") &&
			hasClass(n, "border-b") {
			key, val := extractAlgoraRow(n)
			if key != "" {
				rows[key] = val
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return rows
}

// extractAlgoraRow extracts the key and value from a row div node.
// KEY = first span child with class "text-muted-foreground"
// VALUE = last span child (subtree text, all inner spans concatenated)
func extractAlgoraRow(rowDiv *html.Node) (key, val string) {
	var keyNode, lastSpan *html.Node
	for c := rowDiv.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode || c.Data != "span" {
			continue
		}
		if keyNode == nil && hasClass(c, "text-muted-foreground") {
			keyNode = c
		}
		// Track last span child (may be nested).
		if !hasClass(c, "text-muted-foreground") {
			lastSpan = c
		}
	}
	if keyNode == nil {
		return "", ""
	}
	key = strings.TrimSpace(textContent(keyNode))
	if lastSpan != nil {
		val = strings.TrimSpace(textContent(lastSpan))
	}
	return key, val
}

// applyAlgoraSalary parses "Base Salary" value and populates SalaryMin/Max/Currency/Interval.
// Value may be "$150k - $300k" (split across inner spans → concatenated by textContent).
func applyAlgoraSalary(j *engine.JobListing, salaryText string) {
	if salaryText == "" {
		return
	}

	// Determine currency from $ prefix.
	currency := ""
	if strings.Contains(salaryText, "$") {
		currency = currencyUSD
	}

	// Split on " - " or "–" (en-dash) to get min and max.
	var minStr, maxStr string
	for _, sep := range []string{" - ", " – ", "-", "–"} {
		if idx := strings.Index(salaryText, sep); idx > 0 {
			minStr = strings.TrimSpace(salaryText[:idx])
			maxStr = strings.TrimSpace(salaryText[idx+len(sep):])
			break
		}
	}
	if minStr == "" {
		minStr = salaryText
	}

	minVal := parseDollarAmount(minStr)
	maxVal := parseDollarAmount(maxStr)

	if minVal > 0 {
		j.SalaryMin = &minVal
	}
	if maxVal > 0 {
		j.SalaryMax = &maxVal
	}
	if currency != "" && (minVal > 0 || maxVal > 0) {
		j.SalaryCurrency = currency
		j.SalaryInterval = "year" //nolint:goconst // semantic: SalaryInterval vocabulary — base salary figures are annual
	}
}

// extractAlgoraDescription finds all div.prose.prose-invert nodes and returns
// the text of the LONGEST one (the actual job description).
// NEVER falls back to og:description (which is the company tagline).
// Rationale: Algora job pages have two prose-invert blocks — a short company pitch
// (first) and the full job description (second, longer). Using the longest ensures
// we get the job requirements, not the org blurb.
func extractAlgoraDescription(doc *html.Node) string {
	// Collect all div.prose.prose-invert nodes; pick the longest by raw text length.
	var candidates []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "div" &&
			hasClass(n, "prose") && hasClass(n, "prose-invert") {
			candidates = append(candidates, n)
			// Do NOT recurse into prose-invert; nested prose blocks are uncommon.
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	if len(candidates) == 0 {
		return ""
	}

	// Pick the prose block with the most text content (the job description).
	best := candidates[0]
	bestLen := len(textContent(best))
	for _, n := range candidates[1:] {
		if l := len(textContent(n)); l > bestLen {
			bestLen = l
			best = n
		}
	}

	raw := strings.TrimSpace(textContent(best))
	if raw == "" {
		return ""
	}
	return engine.TruncateRunes(engine.CleanHTML(raw), atsDescriptionMaxRunes, "...")
}
