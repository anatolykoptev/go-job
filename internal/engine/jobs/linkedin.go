package jobs

import (
	"github.com/anatolykoptev/go_job/internal/engine"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"golang.org/x/net/html"
)

// LinkedIn Guest API endpoint — returns HTML, no auth required.
const linkedInGuestAPI = "https://www.linkedin.com/jobs-guest/jobs/api/seeMoreJobPostings/search"

// LinkedIn job view URL for fetching individual job details.
const linkedInJobView = "https://www.linkedin.com/jobs/view/"

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
	"remote": "3",
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

// SearchLinkedInJobs queries the LinkedIn Guest API and returns parsed job cards.
func SearchLinkedInJobs(ctx context.Context, query, location, experience, jobType, remote, timeRange string) ([]LinkedInJob, error) {
	u, err := url.Parse(linkedInGuestAPI)
	if err != nil {
		return nil, err
	}

	q := u.Query()
	q.Set("keywords", query)
	q.Set("sortBy", "DD") // sort by date
	q.Set("start", "0")
	if location != "" {
		q.Set("location", location)
	}
	if v, ok := experienceMap[strings.ToLower(experience)]; ok {
		q.Set("f_E", v)
	}
	if v, ok := jobTypeMap[strings.ToLower(jobType)]; ok {
		q.Set("f_JT", v)
	}
	if v, ok := remoteMap[strings.ToLower(remote)]; ok {
		q.Set("f_WT", v)
	}
	if v, ok := timeRangeMap[strings.ToLower(timeRange)]; ok {
		q.Set("f_TPR", v)
	}
	u.RawQuery = q.Encode()

	body, err := linkedInRequest(ctx, u.String())
	if err != nil {
		return nil, err
	}

	return parseLinkedInHTML(string(body)), nil
}

// linkedInRequest fetches a LinkedIn URL using BrowserClient (Chrome TLS fingerprint)
// when available, falling back to standard net/http client.
// LinkedIn blocks non-browser TLS fingerprints, so BrowserClient is strongly preferred.
func linkedInRequest(ctx context.Context, targetURL string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, engine.Cfg.FetchTimeout)
	defer cancel()

	// Prefer BrowserClient - LinkedIn detects non-browser TLS fingerprints
	if engine.Cfg.BrowserClient != nil {
		headers := engine.ChromeHeaders()
		headers["accept"] = "text/html,application/xhtml+xml,application/xml;q=0.9"
		headers["referer"] = "https://www.linkedin.com/"

		data, err := engine.RetryDo(ctx, engine.DefaultRetryConfig, func() ([]byte, error) {
			d, s, e := engine.Cfg.BrowserClient.Do("GET", targetURL, headers, nil)
			if e != nil {
				return nil, e
			}
			if s != 200 {
				return nil, fmt.Errorf("linkedin status %d", s)
			}
			return d, nil
		})
		if err != nil {
			return nil, err
		}
		return data, nil
	}

	// Fallback: standard HTTP client
	req, err := http.NewRequestWithContext(ctx, "GET", targetURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", engine.UserAgentChrome)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := engine.RetryHTTP(ctx, engine.DefaultRetryConfig, func() (*http.Response, error) {
		return engine.Cfg.HTTPClient.Do(req)
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("linkedin status %d", resp.StatusCode)
	}

	return io.ReadAll(io.LimitReader(resp.Body, 512*1024))
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

	return "", fmt.Errorf("no job details found")
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
		if len(desc) > 3000 {
			desc = desc[:3000] + "..."
		}
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
		html.Render(&sb, c)
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
			time.Sleep(time.Duration(idx) * time.Second) // staggered: 0s, 1s, 2s, 3s, 4s
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
