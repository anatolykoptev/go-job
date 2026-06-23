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
// past the MCP tool deadline when Firebase is slow.
//
// Budget accounting (job_search tool timeout = 3m, see heavyToolTimeouts):
//   - FindWhoIsHiringThread (Algolia, with FetchTimeout 10s + retry): ~15s worst case
//   - searchHNThreadComments (Algolia, with FetchTimeout 10s): ~15s worst case
//   - hnFanoutBudget (Firebase fan-out): 30s
//   - LLM post-processing: ~15s
//   Total worst case: 75s << 3m server-side deadline.
//
// Previously 45s (P0 fix). Reduced to 30s so the full HN path stays well under
// even a conservative 90s client-side per-request deadline (some MCP clients
// set their own timeout shorter than the server ToolTimeout).
//
// A var (not const) so tests can shrink it to exercise the budget-escape path
// with a non-cancellable parent ctx in milliseconds; prod never reassigns it.
var hnFanoutBudget = 30 * time.Second

// hnFanoutBudgetMax is the hard ceiling the runtime budget must stay under so
// the whole HN search path fits within the job_search ToolTimeout. Asserted in
// tests; guards against a future edit pushing the budget past the deadline.
// 90s is the ToolTimeout default; job_search is in heavyToolTimeouts at 3m,
// so there is headroom — but 90s remains the conservative ceiling for test-time
// assertions so hnFanoutBudget is constrained to leave room for pre/post I/O.
const hnFanoutBudgetMax = 90 * time.Second

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
	// caps total wall-time — once spent, fanoutCtx is cancelled, every in-flight
	// fetchHNItem aborts at its next ctx checkpoint, and we return whatever
	// arrived rather than blocking forever.
	fanoutCtx, cancel := context.WithTimeout(ctx, hnFanoutBudget)
	defer cancel()

	type result struct {
		idx  int
		text string
	}
	ch := make(chan result, fetch)
	sem := make(chan struct{}, 10) // max 10 concurrent requests

	// wg tracks every spawned worker so we can JOIN them before returning. This
	// is the deliberate fix for the goroutine-lifetime root (reviewer BLOCKER 2):
	// without the join, workers blocked in stalled I/O outlive the return and
	// keep reading process-global engine.Cfg.HTTPClient (hnjobs.go:118) — a leak
	// of up to hnFanoutBudget per orphan, and a data race against any caller that
	// swaps engine.Cfg after we return (reviewer BLOCKER 1, e.g. a test's
	// t.Cleanup). Joining costs a little latency on the SLOW path only: on the
	// fast path all workers have already sent and exited; on the slow path the
	// collector first hands the caller its partial results at budget, THEN we
	// cancel + Wait, and cancellation makes the drain near-instant.
	var wg sync.WaitGroup
	wg.Add(fetch)

	for i := 0; i < fetch; i++ {
		go func(i int, id int64) {
			defer wg.Done()
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

	// Collect in order. Stop waiting once the fan-out budget is spent: the ch is
	// buffered to fetch, so workers never block on send and Wait() below cannot
	// deadlock even after we stop draining. Slots never filled stay "".
	raw := make([]string, fetch)
	for i := 0; i < fetch; i++ {
		select {
		case r := <-ch:
			raw[r.idx] = r.text
		case <-fanoutCtx.Done():
			slog.Info("hnjobs: fan-out budget exhausted, returning partial",
				slog.Int("collected", i),
				slog.Int("requested", fetch),
				slog.Duration("budget", hnFanoutBudget),
			)
			i = fetch // break the collector loop
		}
	}

	// Join: no worker outlives this call. cancel() (deferred above) has not yet
	// fired, so cancel explicitly here to abort any still-running fetchHNItem,
	// then Wait for every goroutine to finish reading engine.Cfg and exit. After
	// this point nothing in this fan-out touches shared state.
	cancel()
	wg.Wait()

	var comments []string
	for _, t := range raw {
		if t != "" {
			comments = append(comments, t)
		}
	}
	slog.Info("hnjobs: fan-out complete",
		slog.Int64("thread", threadID),
		slog.Int("fetched", fetch),
		slog.Int("non_empty", len(comments)),
	)
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
