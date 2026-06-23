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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anatolykoptev/go_job/internal/engine"
)

const hnFirebaseBase = "https://hacker-news.firebaseio.com/v0"

// hnWhoIsHiringCache caches the thread ID so we don't re-search every call.
var hnWhoIsHiringCache struct {
	mu       sync.Mutex
	threadID int64
	fetchedAt time.Time
}

// hnWhoIsHiringCacheTTL — thread is posted monthly, cache for 6h.
const hnWhoIsHiringCacheTTL = 6 * time.Hour

// hnFanoutBudget caps the total wall-time of the Firebase comment fan-out in
// FetchHNJobComments. A "Who is Hiring" thread can carry 400+ comments, each
// fetched with retry/backoff — without this cap the collector can block well
// past the 90s MCP deadline when Firebase is slow. 45s leaves ample headroom
// for thread-find + Algolia + LLM post-processing within the deadline.
const hnFanoutBudget = 45 * time.Second

// hnItemResponse is the Firebase HN API item shape (story or comment).
type hnItemResponse struct {
	ID    int64   `json:"id"`
	Type  string  `json:"type"`
	By    string  `json:"by"`
	Text  string  `json:"text"`
	Kids  []int64 `json:"kids"`
	Time  int64   `json:"time"`
	Dead  bool    `json:"dead"`
	Deleted bool  `json:"deleted"`
}

// FindWhoIsHiringThread finds the most recent "Who is hiring?" HN thread ID.
// Uses Algolia HN search to locate it, caches result for 6h.
func FindWhoIsHiringThread(ctx context.Context) (int64, error) {
	hnWhoIsHiringCache.mu.Lock()
	defer hnWhoIsHiringCache.mu.Unlock()

	if hnWhoIsHiringCache.threadID != 0 && time.Since(hnWhoIsHiringCache.fetchedAt) < hnWhoIsHiringCacheTTL {
		return hnWhoIsHiringCache.threadID, nil
	}

	u, err := url.Parse(engine.HNAlgoliaByDateURL)
	if err != nil {
		return 0, err
	}
	q := u.Query()
	q.Set("query", "Ask HN: Who is hiring?")
	q.Set("tags", "story,author_whoishiring")
	q.Set("hitsPerPage", "1")
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", engine.UserAgentBot)

	resp, err := engine.RetryHTTP(ctx, engine.DefaultRetryConfig, func() (*http.Response, error) {
		return engine.Cfg.HTTPClient.Do(req) //nolint:gosec // intentional outbound HTTP request
	})
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HN Algolia status %d", resp.StatusCode)
	}

	var data engine.HNAlgoliaResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return 0, err
	}
	if len(data.Hits) == 0 {
		return 0, errors.New("no 'Who is hiring?' thread found")
	}

	var threadID int64
	if _, err := fmt.Sscanf(data.Hits[0].ObjectID, "%d", &threadID); err != nil {
		return 0, fmt.Errorf("parse thread ID: %w", err)
	}

	hnWhoIsHiringCache.threadID = threadID
	hnWhoIsHiringCache.fetchedAt = time.Now()
	slog.Debug("hnjobs: found Who is Hiring thread", slog.Int64("id", threadID))
	return threadID, nil
}

// fetchHNItem fetches a single item from the HN Firebase API.
func fetchHNItem(ctx context.Context, id int64) (*hnItemResponse, error) {
	url := fmt.Sprintf("%s/item/%d.json", hnFirebaseBase, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", engine.UserAgentBot)

	resp, err := engine.RetryHTTP(ctx, engine.DefaultRetryConfig, func() (*http.Response, error) {
		return engine.Cfg.HTTPClient.Do(req) //nolint:gosec // intentional outbound HTTP request
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, err
	}

	var item hnItemResponse
	if err := json.Unmarshal(body, &item); err != nil {
		return nil, err
	}
	return &item, nil
}

// FetchHNJobComments fetches top-level job comments from a "Who is Hiring" thread.
// Returns up to limit raw comment texts (HTML stripped).
func FetchHNJobComments(ctx context.Context, threadID int64, limit int) ([]string, error) {
	thread, err := fetchHNItem(ctx, threadID)
	if err != nil {
		return nil, fmt.Errorf("fetch thread: %w", err)
	}
	if len(thread.Kids) == 0 {
		return nil, errors.New("thread has no comments")
	}

	// Fetch comments in parallel, up to limit*2 (we'll filter down).
	fetch := limit * 2
	if fetch > len(thread.Kids) {
		fetch = len(thread.Kids)
	}

	// Bound the whole fan-out. A "Who is Hiring" thread can carry 400+ kids;
	// each fetchHNItem retries with backoff, so an unbounded collector can run
	// far past the MCP deadline if Firebase is slow/throttling. hnFanoutBudget
	// caps total wall-time — cancelled per-item fetches return empty and the
	// collector returns whatever arrived in time rather than blocking forever.
	fanoutCtx, cancel := context.WithTimeout(ctx, hnFanoutBudget)
	defer cancel()

	type result struct {
		idx  int
		text string
	}
	ch := make(chan result, fetch)
	sem := make(chan struct{}, 10) // max 10 concurrent requests

	for i := 0; i < fetch; i++ {
		go func(i int, id int64) {
			sem <- struct{}{}
			defer func() { <-sem }()

			// Stagger requests slightly to avoid hammering Firebase.
			delay := time.Duration(i/10) * 200 * time.Millisecond
			if delay > 0 {
				select {
				case <-time.After(delay):
				case <-fanoutCtx.Done():
					ch <- result{i, ""}
					return
				}
			}

			item, err := fetchHNItem(fanoutCtx, id)
			if err != nil || item == nil || item.Dead || item.Deleted || item.Text == "" {
				ch <- result{i, ""}
				return
			}
			text := engine.CleanHTML(item.Text)
			if len(text) > 1200 {
				text = text[:1200] + "..."
			}
			ch <- result{i, text}
		}(i, thread.Kids[i])
	}

	// Collect in order. Stop waiting once the fan-out budget is spent: the
	// remaining goroutines are cancelled via fanoutCtx and will exit, but we
	// must not block on them — return what we have. Slots never filled stay "".
	raw := make([]string, fetch)
	for i := 0; i < fetch; i++ {
		select {
		case r := <-ch:
			raw[r.idx] = r.text
		case <-fanoutCtx.Done():
			slog.Debug("hnjobs: fan-out budget exhausted, returning partial",
				slog.Int("collected", i),
				slog.Int("requested", fetch),
			)
			i = fetch // break the collector loop
		}
	}

	var comments []string
	for _, t := range raw {
		if t != "" {
			comments = append(comments, t)
		}
	}
	return comments, nil
}

// FilterHNJobComments filters comment texts by keyword match.
func FilterHNJobComments(comments []string, query string) []string {
	if query == "" {
		return comments
	}
	keywords := strings.Fields(strings.ToLower(query))
	if len(keywords) == 0 {
		return comments
	}

	var filtered []string
	for _, c := range comments {
		lower := strings.ToLower(c)
		for _, kw := range keywords {
			if strings.Contains(lower, kw) {
				filtered = append(filtered, c)
				break
			}
		}
	}
	return filtered
}

// SearchHNJobs fetches job comments from the latest "Who is Hiring" thread matching query.
// Uses Algolia search within the thread for keyword matching (efficient, handles large threads).
// Falls back to sequential Firebase fetch if Algolia returns nothing.
func SearchHNJobs(ctx context.Context, query string, limit int) ([]engine.SearxngResult, error) {
	engine.IncrHNJobsRequests()

	threadID, err := FindWhoIsHiringThread(ctx)
	if err != nil {
		return nil, fmt.Errorf("find thread: %w", err)
	}

	threadURL := fmt.Sprintf("https://news.ycombinator.com/item?id=%d", threadID)

	// Primary: Algolia search within thread comments (searches entire thread by keyword).
	comments, err := searchHNThreadComments(ctx, threadID, query, limit*2)
	if err != nil {
		slog.Debug("hnjobs: algolia search failed, falling back to Firebase", slog.Any("error", err))
		comments = nil
	}

	// Fallback: sequential Firebase fetch + keyword filter.
	if len(comments) == 0 {
		raw, err := FetchHNJobComments(ctx, threadID, limit*4)
		if err != nil {
			return nil, fmt.Errorf("fetch comments: %w", err)
		}
		comments = FilterHNJobComments(raw, query)
	}

	if len(comments) > limit {
		comments = comments[:limit]
	}

	slog.Debug("hnjobs: search complete",
		slog.Int64("thread", threadID),
		slog.Int("results", len(comments)),
	)

	results := make([]engine.SearxngResult, len(comments))
	for i, text := range comments {
		title := extractHNJobTitle(text)
		results[i] = engine.SearxngResult{
			Title:   title,
			Content: "**Source:** HN Who is Hiring\n\n" + text,
			URL:     threadURL,
			Score:   0.8,
		}
	}
	return results, nil
}

// searchHNThreadComments uses Algolia to search within a specific HN story's comments.
// This searches the entire thread (potentially 400+ comments) by keyword in one API call.
func searchHNThreadComments(ctx context.Context, threadID int64, query string, limit int) ([]string, error) {
	if query == "" {
		return nil, nil
	}

	u, err := url.Parse(engine.HNAlgoliaURL)
	if err != nil {
		return nil, err
	}

	q := u.Query()
	q.Set("query", query)
	q.Set("tags", fmt.Sprintf("comment,story_%d", threadID))
	q.Set("hitsPerPage", strconv.Itoa(limit))
	u.RawQuery = q.Encode()

	// Per-call timeout + retry so a stalled Algolia endpoint cannot hang the
	// whole HN search past the MCP deadline (matches fetchHNItem above and the
	// engine.RetryHTTP+FetchTimeout idiom used across the jobs package).
	reqCtx, cancel := context.WithTimeout(ctx, engine.Cfg.FetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", engine.UserAgentBot)

	resp, err := engine.RetryHTTP(reqCtx, engine.DefaultRetryConfig, func() (*http.Response, error) {
		return engine.Cfg.HTTPClient.Do(req) //nolint:gosec // intentional outbound HTTP request
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("algolia thread search status %d", resp.StatusCode)
	}

	var data engine.HNAlgoliaResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}

	var comments []string
	for _, hit := range data.Hits {
		text := engine.CleanHTML(hit.CommentText)
		if text == "" {
			continue
		}
		if len(text) > 1200 {
			text = text[:1200] + "..."
		}
		comments = append(comments, text)
	}
	return comments, nil
}

// extractHNJobTitle extracts a short title from a HN job comment.
// HN job posts typically start with "Company | Role | Location | ..."
func extractHNJobTitle(text string) string {
	lines := strings.SplitN(text, "\n", 3)
	if len(lines) > 0 {
		first := strings.TrimSpace(lines[0])
		if len(first) > 80 {
			first = first[:80] + "..."
		}
		if first != "" {
			return first
		}
	}
	if len(text) > 80 {
		return text[:80] + "..."
	}
	return text
}
