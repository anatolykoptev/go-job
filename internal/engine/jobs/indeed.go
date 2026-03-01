package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/anatolykoptev/go_job/internal/engine"
	"golang.org/x/net/html"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
)

// --- Indeed GraphQL API (internal iOS app endpoint, hardcoded key) ---

const (
	indeedGraphQLEndpoint = "https://apis.indeed.com/graphql"
	// indeedIOSUserAgent and indeedAppInfo mimic the Indeed iOS app.
	indeedIOSUserAgent = "Mozilla/5.0 (iPhone; CPU iPhone OS 16_6_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148 Indeed App 193.1"
	indeedAppInfo      = "appv=193.1; appid=com.indeed.jobsearch; osv=16.6.1; os=ios; dtype=phone"
	// indeedSiteSearch is used for the SearXNG fallback.
	indeedSiteSearch = "site:indeed.com/viewjob"
)

// indeedDateRanges maps human-readable time ranges to Indeed GraphQL filter values.
var indeedDateRanges = map[string]string{
	"day":   "24h",
	"week":  "7d",
	"month": "30d",
}

// --- GraphQL request/response types ---

type indeedGraphQLRequest struct {
	Query string `json:"query"`
}

type indeedGraphQLResponse struct {
	Data struct {
		JobSearch struct {
			PageInfo struct {
				NextCursor string `json:"nextCursor"`
			} `json:"pageInfo"`
			Results []struct {
				Job indeedGQLJob `json:"job"`
			} `json:"results"`
		} `json:"jobSearch"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

type indeedGQLJob struct {
	Key           string `json:"key"`
	Title         string `json:"title"`
	DatePublished string `json:"datePublished"`
	Location      struct {
		City      string `json:"city"`
		Admin1    string `json:"admin1Code"`
		Formatted struct {
			Short string `json:"short"`
		} `json:"formatted"`
	} `json:"location"`
	Compensation struct {
		BaseSalary struct {
			UnitOfWork string             `json:"unitOfWork"`
			Range      *indeedSalaryRange `json:"range"`
		} `json:"baseSalary"`
		Estimated struct {
			BaseSalary struct {
				Range *indeedSalaryRange `json:"range"`
			} `json:"baseSalary"`
		} `json:"estimated"`
		CurrencyCode string `json:"currencyCode"`
	} `json:"compensation"`
	Employer struct {
		Name string `json:"name"`
	} `json:"employer"`
	Description struct {
		HTML string `json:"html"`
	} `json:"description"`
}

type indeedSalaryRange struct {
	Min float64 `json:"min"`
	Max float64 `json:"max"`
}

// buildIndeedGraphQLQuery constructs the GraphQL query string for Indeed job search.
func buildIndeedGraphQLQuery(what, where, timeRange string, limit int, cursor string) string {
	var args []string
	args = append(args, fmt.Sprintf("what: %q", what))
	if where != "" {
		args = append(args, fmt.Sprintf(`location: { where: %q, radius: 50, radiusUnit: MILES }`, where))
	}
	args = append(args, fmt.Sprintf("limit: %d", limit))
	args = append(args, "sort: RELEVANCE")
	if cursor != "" {
		args = append(args, fmt.Sprintf("cursor: %q", cursor))
	}
	if dr, ok := indeedDateRanges[strings.ToLower(timeRange)]; ok {
		args = append(args, fmt.Sprintf(`filters: { date: { field: "dateOnIndeed", start: %q } }`, dr))
	}

	return fmt.Sprintf(`query GetJobData { jobSearch(%s) {
  pageInfo { nextCursor }
  results { job {
    key title datePublished
    location { city admin1Code formatted { short } }
    compensation {
      baseSalary { unitOfWork range { ... on Range { min max } } }
      estimated { baseSalary { unitOfWork range { ... on Range { min max } } } }
      currencyCode
    }
    employer { name }
    description { html }
  } }
} }`, strings.Join(args, ", "))
}

// doIndeedGraphQL executes a GraphQL request against the Indeed internal API.
func doIndeedGraphQL(ctx context.Context, gqlQuery string) (*indeedGraphQLResponse, error) {
	apiKey := engine.Cfg.IndeedAPIKey
	if apiKey == "" {
		return nil, errors.New("indeed: no API key configured")
	}

	bodyBytes, err := json.Marshal(indeedGraphQLRequest{Query: gqlQuery})
	if err != nil {
		return nil, fmt.Errorf("indeed: marshal request: %w", err)
	}

	headers := map[string]string{
		"content-type":    "application/json",
		"indeed-api-key":  apiKey,
		"user-agent":      indeedIOSUserAgent,
		"indeed-app-info": indeedAppInfo,
		"indeed-locale":   "en-US",
		"indeed-co":       "us",
		"Host":            "apis.indeed.com",
	}

	respBytes, err := engine.RetryDo(ctx, engine.DefaultRetryConfig, func() ([]byte, error) {
		if engine.Cfg.BrowserClient != nil {
			data, _, status, e := engine.Cfg.BrowserClient.Do("POST", indeedGraphQLEndpoint, headers, bytes.NewReader(bodyBytes))
			if e != nil {
				return nil, e
			}
			if status != http.StatusOK {
				return nil, fmt.Errorf("indeed graphql status %d", status)
			}
			return data, nil
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, indeedGraphQLEndpoint, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, err
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := engine.Cfg.HTTPClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("indeed graphql status %d", resp.StatusCode)
		}
		return io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	})
	if err != nil {
		return nil, err
	}

	var gqlResp indeedGraphQLResponse
	if err := json.Unmarshal(respBytes, &gqlResp); err != nil {
		return nil, fmt.Errorf("indeed: parse response: %w", err)
	}
	if len(gqlResp.Errors) > 0 {
		return nil, fmt.Errorf("indeed graphql error: %s", gqlResp.Errors[0].Message)
	}
	return &gqlResp, nil
}

// indeedGQLJobToResult converts a GraphQL job into a SearxngResult for the pipeline.
func indeedGQLJobToResult(job indeedGQLJob) engine.SearxngResult {
	location := job.Location.Formatted.Short
	if location == "" {
		location = job.Location.City
		if job.Location.Admin1 != "" {
			location += ", " + job.Location.Admin1
		}
	}

	// Format salary from baseSalary or estimated
	salary := ""
	comp := job.Compensation
	salaryRange := comp.BaseSalary.Range
	if salaryRange == nil {
		salaryRange = comp.Estimated.BaseSalary.Range
	}
	if salaryRange != nil && (salaryRange.Min > 0 || salaryRange.Max > 0) {
		unit := comp.BaseSalary.UnitOfWork
		if unit == "" {
			unit = "year"
		}
		curr := comp.CurrencyCode
		if curr == "" {
			curr = "USD"
		}
		salary = fmt.Sprintf("%.0f–%.0f %s/%s", salaryRange.Min, salaryRange.Max, curr, unit)
	}

	// Convert HTML description to markdown
	desc := ""
	if job.Description.HTML != "" {
		md, err := htmltomarkdown.ConvertString(job.Description.HTML)
		if err == nil {
			desc = engine.TruncateRunes(md, 2500, "...")
		}
	}

	jobURL := "https://www.indeed.com/viewjob?jk=" + job.Key

	var contentParts []string
	contentParts = append(contentParts, "**Source:** Indeed")
	if job.Employer.Name != "" {
		contentParts = append(contentParts, "**Company:** "+job.Employer.Name)
	}
	if location != "" {
		contentParts = append(contentParts, "**Location:** "+location)
	}
	if salary != "" {
		contentParts = append(contentParts, "**Salary:** "+salary)
	}
	if job.DatePublished != "" {
		contentParts = append(contentParts, "**Posted:** "+job.DatePublished)
	}
	if desc != "" {
		contentParts = append(contentParts, "\n"+desc)
	}

	title := job.Title
	if job.Employer.Name != "" {
		title = job.Title + " at " + job.Employer.Name
	}

	return engine.SearxngResult{
		Title:   title,
		Content: strings.Join(contentParts, "\n"),
		URL:     jobURL,
	}
}

// searchIndeedGraphQL fetches jobs from Indeed's internal GraphQL API.
// Returns up to limit results, fetching multiple pages if needed.
func searchIndeedGraphQL(ctx context.Context, query, location, timeRange string, limit int) ([]engine.SearxngResult, error) {
	pageLimit := limit
	if pageLimit > 100 {
		pageLimit = 100
	}
	if pageLimit <= 0 {
		pageLimit = 15
	}

	gqlQuery := buildIndeedGraphQLQuery(query, location, timeRange, pageLimit, "")
	resp, err := doIndeedGraphQL(ctx, gqlQuery)
	if err != nil {
		return nil, err
	}

	var results []engine.SearxngResult
	for _, r := range resp.Data.JobSearch.Results {
		if r.Job.Key == "" {
			continue
		}
		results = append(results, indeedGQLJobToResult(r.Job))
	}

	slog.Debug("indeed: graphql search complete", slog.Int("results", len(results)))
	return results, nil
}

// SearchIndeedJobs is the main entry point for Indeed job search.
// Tries the GraphQL API first, falls back to SearXNG site: search.
func SearchIndeedJobs(ctx context.Context, query, location string, limit int) ([]engine.SearxngResult, error) {
	return SearchIndeedJobsFiltered(ctx, query, location, "", "", limit)
}

// SearchIndeedJobsFiltered searches Indeed with optional jobType and timeRange filters.
func SearchIndeedJobsFiltered(ctx context.Context, query, location, jobType, timeRange string, limit int) ([]engine.SearxngResult, error) {
	engine.IncrIndeedRequests()

	// Try GraphQL API first (direct, no SearXNG dependency)
	results, err := searchIndeedGraphQL(ctx, query, location, timeRange, limit)
	if err != nil {
		slog.Warn("indeed: GraphQL API failed, falling back to SearXNG", slog.Any("error", err))
	} else if len(results) > 0 {
		return results, nil
	}

	// Fallback: SearXNG site: search (original approach)
	return searchIndeedViaSearxng(ctx, query, location, limit)
}

// searchIndeedViaSearxng is the original SearXNG-based Indeed search (fallback).
func searchIndeedViaSearxng(ctx context.Context, query, location string, limit int) ([]engine.SearxngResult, error) {
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
		r, err := engine.SearchSearXNG(ctx, searxQuery, "all", "", engine.DefaultSearchEngine)
		gCh <- searchRes{r, err}
	}()
	go func() {
		r, err := engine.SearchSearXNG(ctx, searxQuery, "all", "", engine.DefaultSearchEngine)
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

	slog.Debug("indeed: searxng search complete", slog.Int("results", len(merged)))
	return merged, nil
}

// indeedRequest fetches an Indeed URL using BrowserClient (Chrome TLS fingerprint)
// when available, falling back to engine.FetchURLContent.
// Indeed blocks non-browser TLS fingerprints similarly to LinkedIn.
func indeedRequest(ctx context.Context, targetURL string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, engine.Cfg.FetchTimeout)
	defer cancel()

	if engine.Cfg.BrowserClient != nil {
		headers := engine.ChromeHeaders()
		headers["referer"] = "https://www.indeed.com/"
		headers["accept"] = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"

		data, err := engine.RetryDo(ctx, engine.DefaultRetryConfig, func() ([]byte, error) {
			d, _, s, e := engine.Cfg.BrowserClient.Do("GET", targetURL, headers, nil)
			if e != nil {
				return nil, e
			}
			if s != http.StatusOK {
				return nil, fmt.Errorf("indeed status %d", s)
			}
			return d, nil
		})
		if err != nil {
			return "", err
		}
		return string(data), nil
	}

	_, text, err := engine.FetchURLContent(ctx, targetURL)
	return text, err
}

// fetchIndeedJobContent fetches an Indeed job page and extracts structured content.
func fetchIndeedJobContent(ctx context.Context, r engine.SearxngResult) string {
	bodyText, err := indeedRequest(ctx, r.URL)
	if err != nil || bodyText == "" {
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
		"jobsearch-JobInfoHeader-title":          "**Title:**",
		"inlineHeader-companyName":               "**Company:**",
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
