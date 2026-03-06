package websearch

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

const soAPIBase = "https://api.stackexchange.com/2.3"

// StackOverflow searches StackExchange API v2.3. No auth required (300 req/day).
type StackOverflow struct {
	baseURL    string
	httpClient *http.Client
}

// StackOverflowOption configures StackOverflow.
type StackOverflowOption func(*StackOverflow)

// WithSOBaseURL overrides the default SE API base URL (for testing).
func WithSOBaseURL(u string) StackOverflowOption {
	return func(s *StackOverflow) { s.baseURL = u }
}

// WithSOHTTPClient sets a custom HTTP client.
func WithSOHTTPClient(c *http.Client) StackOverflowOption {
	return func(s *StackOverflow) { s.httpClient = c }
}

// NewStackOverflow creates a StackExchange API client.
func NewStackOverflow(opts ...StackOverflowOption) *StackOverflow {
	s := &StackOverflow{baseURL: soAPIBase, httpClient: &http.Client{}}
	for _, o := range opts {
		o(s)
	}
	return s
}

// soDaily tracks daily request count (300/day free tier).
var soDaily struct {
	count atomic.Int64
	day   atomic.Int32
}

const soDailyLimit = 300

// soCheckLimit enforces the daily quota. Returns true if under limit.
func soCheckLimit() bool {
	today := int32(time.Now().UTC().YearDay())
	if stored := soDaily.day.Load(); stored != today {
		soDaily.day.CompareAndSwap(stored, today)
		soDaily.count.Store(0)
	}
	return soDaily.count.Add(1) <= soDailyLimit
}

// Search implements Provider. Queries Stack Exchange search/advanced endpoint.
func (so *StackOverflow) Search(ctx context.Context, query string, opts SearchOpts) ([]Result, error) {
	if !soCheckLimit() {
		return nil, nil // silently skip when over daily budget
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}

	u := fmt.Sprintf("%s/search/advanced?%s", so.baseURL, url.Values{
		"q":        {query},
		"site":     {"stackoverflow"},
		"order":    {"desc"},
		"sort":     {"relevance"},
		"pagesize": {fmt.Sprintf("%d", limit)},
		"filter":   {"withbody"},
	}.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("stackoverflow: build request: %w", err)
	}

	resp, err := so.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("stackoverflow: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("stackoverflow: status %d", resp.StatusCode)
	}

	body, err := soReadBody(resp)
	if err != nil {
		return nil, fmt.Errorf("stackoverflow: read body: %w", err)
	}

	results, err := ParseStackOverflowJSON(body)
	if err != nil {
		return nil, fmt.Errorf("stackoverflow: %w", err)
	}

	return applyLimit(results, limit), nil
}

// soReadBody handles gzip-compressed responses (SE API always gzips).
func soReadBody(resp *http.Response) ([]byte, error) {
	if resp.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("gzip open: %w", err)
		}
		defer gz.Close()
		return io.ReadAll(gz)
	}
	return io.ReadAll(resp.Body)
}

// soResponse is the SE API response wrapper.
type soResponse struct {
	Items          []soItem `json:"items"`
	QuotaRemaining int      `json:"quota_remaining"`
}

type soItem struct {
	Title       string   `json:"title"`
	Link        string   `json:"link"`
	Score       int      `json:"score"`
	AnswerCount int      `json:"answer_count"`
	Tags        []string `json:"tags"`
	IsAnswered  bool     `json:"is_answered"`
}

// ParseStackOverflowJSON parses SE API JSON into search results.
func ParseStackOverflowJSON(data []byte) ([]Result, error) {
	var resp soResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("parse json: %w", err)
	}

	results := make([]Result, 0, len(resp.Items))
	for _, item := range resp.Items {
		if item.Title == "" || item.Link == "" {
			continue
		}
		answered := "unanswered"
		if item.IsAnswered {
			answered = "answered"
		}
		content := fmt.Sprintf("[%s] | %d pts | %d answers | %s",
			strings.Join(item.Tags, ", "), item.Score, item.AnswerCount, answered)

		results = append(results, Result{
			Title:    item.Title,
			URL:      item.Link,
			Content:  content,
			Score:    directResultScore,
			Metadata: map[string]string{"engine": "stackoverflow"},
		})
	}
	return results, nil
}
