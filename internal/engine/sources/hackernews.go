package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// hnTimeFilter returns a fresh numeric filter for the given time range.
// Computes timestamps at call time (not package init) to avoid staleness.
func hnTimeFilter(timeRange string) string {
	var d time.Duration
	switch strings.ToLower(timeRange) {
	case "day":
		d = 24 * time.Hour
	case "week":
		d = 7 * 24 * time.Hour
	case "month":
		d = 30 * 24 * time.Hour
	case "year":
		d = 365 * 24 * time.Hour
	default:
		return ""
	}
	return fmt.Sprintf("created_at_i>%d", time.Now().Add(-d).Unix())
}

// SearchHackerNews queries the HN Algolia API and returns results.
func SearchHackerNews(ctx context.Context, input engine.HNSearchInput) ([]engine.HNResult, error) {
	// Always use relevance-based search; time filter is applied via numericFilters.
	timeFilter := hnTimeFilter(input.TimeRange)

	u, err := url.Parse(engine.HNAlgoliaURL)
	if err != nil {
		return nil, err
	}

	q := u.Query()
	q.Set("query", input.Query)
	q.Set("tags", "story")
	q.Set("hitsPerPage", "20")
	if timeFilter != "" {
		q.Set("numericFilters", timeFilter)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", engine.UserAgentBot)

	resp, err := engine.RetryHTTP(ctx, engine.DefaultRetryConfig, func() (*http.Response, error) {
		return engine.Cfg.HTTPClient.Do(req)
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HN Algolia API returned status %d", resp.StatusCode)
	}

	var data engine.HNAlgoliaResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}

	var results []engine.HNResult
	for _, hit := range data.Hits {
		hnURL := hit.URL
		if hnURL == "" {
			hnURL = fmt.Sprintf("https://news.ycombinator.com/item?id=%s", hit.ObjectID)
		}

		snippet := engine.CleanHTML(hit.StoryText)
		if len(snippet) > 500 {
			snippet = snippet[:500] + "..."
		}

		results = append(results, engine.HNResult{
			ObjectID:    hit.ObjectID,
			Title:       hit.Title,
			URL:         hnURL,
			Author:      hit.Author,
			Points:      hit.Points,
			NumComments: hit.NumComments,
			CreatedAt:   time.Unix(hit.CreatedAtI, 0).UTC().Format("2006-01-02"),
			Snippet:     snippet,
		})
	}

	slog.Debug("hackernews: search complete", slog.String("query", input.Query), slog.Int("results", len(results)))
	return results, nil
}

// fetchHNComments fetches top comments for a story via Algolia API.
// Returns up to maxComments comments sorted by points (most relevant first).
func fetchHNComments(ctx context.Context, storyID string, maxComments int) ([]string, error) {
	u := fmt.Sprintf("https://hn.algolia.com/api/v1/search?tags=comment,story_%s&hitsPerPage=%d", storyID, maxComments*2)

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", engine.UserAgentBot)

	resp, err := engine.Cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HN comments API status %d", resp.StatusCode)
	}

	var data engine.HNAlgoliaResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}

	// Sort by points (highest first) for most relevant comments
	sort.Slice(data.Hits, func(i, j int) bool {
		return data.Hits[i].Points > data.Hits[j].Points
	})

	var comments []string
	for _, hit := range data.Hits {
		text := engine.CleanHTML(hit.CommentText)
		if text == "" {
			continue
		}
		if len(text) > 800 {
			text = text[:800] + "..."
		}
		comment := fmt.Sprintf("[%s, %d pts]: %s", hit.Author, hit.Points, text)
		comments = append(comments, comment)
		if len(comments) >= maxComments {
			break
		}
	}

	return comments, nil
}

// engine.SummarizeHNResults converts HN results to SearxngResults, fetches top comments,
// and runs LLM summarization with rich discussion context.
func SummarizeHNResults(ctx context.Context, query string, results []engine.HNResult) (engine.HNSearchOutput, error) {
	if len(results) == 0 {
		return engine.HNSearchOutput{Query: query, Summary: "No HackerNews results found."}, nil
	}

	// Fetch top comments for the most-discussed stories (parallel, up to 3 stories)
	type commentResult struct {
		storyIdx int
		comments []string
	}

	// Pick top stories by comment count
	type storyIdx struct {
		idx      int
		comments int
	}
	var topStories []storyIdx
	for i, r := range results {
		if r.NumComments > 5 {
			topStories = append(topStories, storyIdx{i, r.NumComments})
		}
	}
	sort.Slice(topStories, func(i, j int) bool {
		return topStories[i].comments > topStories[j].comments
	})
	if len(topStories) > 3 {
		topStories = topStories[:3]
	}

	commentMap := make(map[int][]string)
	if len(topStories) > 0 {
		var wg sync.WaitGroup
		var mu sync.Mutex
		for _, ts := range topStories {
			if results[ts.idx].ObjectID == "" { continue }
			wg.Add(1)
			go func(idx int, objectID string) {
				defer wg.Done()
				comments, err := fetchHNComments(ctx, objectID, 5)
				if err != nil {
					slog.Debug("hn: failed to fetch comments", slog.String("story", objectID), slog.Any("error", err))
					return
				}
				mu.Lock()
				commentMap[idx] = comments
				mu.Unlock()
			}(ts.idx, results[ts.idx].ObjectID)
		}
		wg.Wait()
	}

	// Build rich sources text with comments
	var searxResults []engine.SearxngResult
	contents := make(map[string]string)
	for i, r := range results {
		content := fmt.Sprintf("%d points | %d comments | by %s | %s", r.Points, r.NumComments, r.Author, r.CreatedAt)
		if r.Snippet != "" {
			content += "\n" + r.Snippet
		}
		searxResults = append(searxResults, engine.SearxngResult{
			Title:   r.Title,
			URL:     r.URL,
			Content: content,
		})

		// Add fetched comments as page content
		if comments, ok := commentMap[i]; ok && len(comments) > 0 {
			contents[r.URL] = "Top community comments:\n" + strings.Join(comments, "\n\n")
		}
	}

	sources := engine.BuildSourcesText(searxResults, contents, engine.Cfg.MaxContentChars)
	prompt := fmt.Sprintf(hnSummarizePrompt, query, sources)

	raw, err := engine.CallLLM(ctx, prompt)
	if err != nil {
		return engine.HNSearchOutput{Query: query, Results: results, Summary: "LLM summarization failed: " + err.Error()}, nil
	}

	var out engine.LLMStructuredOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		if answer := engine.ExtractJSONAnswer(raw); answer != "" {
			return engine.HNSearchOutput{Query: query, Results: results, Summary: answer}, nil
		}
		return engine.HNSearchOutput{Query: query, Results: results, Summary: raw}, nil
	}

	return engine.HNSearchOutput{Query: query, Results: results, Summary: out.Answer}, nil
}

const hnSummarizePrompt = `You are a tech news analyst summarizing HackerNews discussions.

Respond with valid JSON only (no markdown wrapping):
{"answer": "your summary here"}

Rules:
- Summarize the key themes, opinions, and insights from the HN discussions
- When community comments are available, synthesize the main arguments and perspectives
- Note any consensus or disagreements in the community
- Highlight the most upvoted/discussed posts
- Cite sources using [1], [2], etc.
- Keep the summary detailed and actionable (500-1500 chars)
- Answer in the SAME LANGUAGE as the query

Query: %s

Sources:
%s`
