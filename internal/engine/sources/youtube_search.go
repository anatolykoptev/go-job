package sources

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
	"sync"
	"time"
)

// YouTube search — Data API v3 with scraping fallback, parallel transcript fetching,
// and LLM summarization.

const (
	ytDataAPIBase       = "https://www.googleapis.com/youtube/v3"
	ytInitialDataMarker = "var ytInitialData = "
	ytSearchFilter      = "EgIQAQ%3D%3D" // videos-only filter param
	ytTranscriptMaxLen  = 8000           // max transcript chars sent to LLM
)

var videoIDRE = regexp.MustCompile(`(?:youtube\.com/watch\?(?:.*&)?v=|youtu\.be/)([a-zA-Z0-9_-]{11})`)

// extractVideoID pulls the 11-char video ID from any YouTube URL format.
func extractVideoID(rawURL string) string {
	m := videoIDRE.FindStringSubmatch(rawURL)
	if len(m) >= 2 {
		return m[1]
	}
	return ""
}

// --- YouTube Data API v3 types ---

type ytDataSearchResp struct {
	Items []ytDataItem `json:"items"`
}

type ytDataItem struct {
	ID      ytDataItemID      `json:"id"`
	Snippet ytDataItemSnippet `json:"snippet"`
}

type ytDataItemID struct {
	VideoID string `json:"videoId"`
}

type ytDataItemSnippet struct {
	Title        string `json:"title"`
	Description  string `json:"description"`
	ChannelTitle string `json:"channelTitle"`
}

// --- ytInitialData scraping types ---

type ytSearchResult struct {
	VideoRenderer *struct {
		VideoID string `json:"videoId"`
		Title   struct {
			Runs []struct{ Text string } `json:"runs"`
		} `json:"title"`
		OwnerText struct {
			Runs []struct{ Text string } `json:"runs"`
		} `json:"ownerText"`
		DescriptionSnippet *struct {
			Runs []struct{ Text string } `json:"runs"`
		} `json:"descriptionSnippet"`
	} `json:"videoRenderer"`
}

// YouTubeTranscriptsEnabled reports whether transcript fetching is enabled in config.
func YouTubeTranscriptsEnabled() bool {
	return engine.Cfg.YouTubeTranscriptsEnabled
}

// SearchYouTube searches YouTube videos.
// Uses YouTube Data API v3 when a key is configured; otherwise scrapes ytInitialData.
func SearchYouTube(ctx context.Context, query, language string, limit int) ([]engine.YouTubeVideo, error) {
	engine.IncrYouTubeSearch()
	if limit <= 0 || limit > 10 {
		limit = 5
	}
	if engine.Cfg.YouTubeAPIKey != "" {
		return searchYouTubeDataAPI(ctx, query, language, limit)
	}
	return searchYouTubeInitialData(ctx, query, limit)
}

// searchYouTubeDataAPI searches via YouTube Data API v3.
// Automatically falls back to the secondary key on quota errors (403).
func searchYouTubeDataAPI(ctx context.Context, query, language string, limit int) ([]engine.YouTubeVideo, error) {
	keys := []string{engine.Cfg.YouTubeAPIKey}
	if engine.Cfg.YouTubeAPIKeyFallback != "" {
		keys = append(keys, engine.Cfg.YouTubeAPIKeyFallback)
	}
	var lastErr error
	for _, key := range keys {
		videos, err := doYouTubeDataSearch(ctx, query, language, limit, key)
		if err == nil {
			return videos, nil
		}
		lastErr = err
		slog.Debug("youtube data API key failed, trying fallback", slog.Any("err", err))
	}
	return nil, lastErr
}

func doYouTubeDataSearch(ctx context.Context, query, language string, limit int, apiKey string) ([]engine.YouTubeVideo, error) {
	params := url.Values{}
	params.Set("part", "snippet")
	params.Set("q", query)
	params.Set("type", "video")
	params.Set("maxResults", fmt.Sprintf("%d", limit))
	params.Set("key", apiKey)
	if language != "" && language != "all" {
		params.Set("relevanceLanguage", language)
	}

	apiURL := ytDataAPIBase + "/search?" + params.Encode()
	resp, err := engine.RetryHTTP(ctx, engine.DefaultRetryConfig, func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", engine.UserAgentBot)
		return engine.Cfg.HTTPClient.Do(req)
	})
	if err != nil {
		return nil, fmt.Errorf("youtube data API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("youtube data API %d: %s", resp.StatusCode, string(body))
	}

	var result ytDataSearchResp
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode youtube data API: %w", err)
	}

	videos := make([]engine.YouTubeVideo, 0, len(result.Items))
	for _, item := range result.Items {
		if item.ID.VideoID == "" {
			continue
		}
		snippet := engine.Truncate(item.Snippet.ChannelTitle+": "+item.Snippet.Description, 200)
		videos = append(videos, engine.YouTubeVideo{
			ID:      item.ID.VideoID,
			Title:   item.Snippet.Title,
			URL:     "https://www.youtube.com/watch?v=" + item.ID.VideoID,
			Snippet: snippet,
		})
	}
	return videos, nil
}

// searchYouTubeInitialData scrapes YouTube search results by parsing ytInitialData.
func searchYouTubeInitialData(ctx context.Context, query string, limit int) ([]engine.YouTubeVideo, error) {
	searchURL := "https://www.youtube.com/results?search_query=" + url.QueryEscape(query) + "&sp=" + ytSearchFilter

	resp, err := engine.RetryHTTP(ctx, engine.DefaultRetryConfig, func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, "GET", searchURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", engine.RandomUserAgent())
		req.Header.Set("Accept-Language", "en-US,en;q=0.9")
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
		return engine.Cfg.HTTPClient.Do(req)
	})
	if err != nil {
		return nil, fmt.Errorf("youtube search page: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read youtube search response: %w", err)
	}

	idx := strings.Index(string(body), ytInitialDataMarker)
	if idx < 0 {
		return nil, fmt.Errorf("ytInitialData not found in YouTube search response")
	}
	jsonData := extractJSON(body[idx+len(ytInitialDataMarker):])
	if jsonData == nil {
		return nil, fmt.Errorf("failed to extract ytInitialData JSON")
	}
	return extractVideosFromInitialData(jsonData, limit), nil
}

// extractJSON extracts a complete JSON object starting at b[0] == '{' by tracking brace depth.
func extractJSON(b []byte) []byte {
	if len(b) == 0 || b[0] != '{' {
		return nil
	}
	depth := 0
	inStr := false
	var prev byte
	for i, c := range b {
		if inStr {
			if c == '"' && prev != '\\' {
				inStr = false
			}
		} else {
			switch c {
			case '"':
				inStr = true
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					return b[:i+1]
				}
			}
		}
		prev = c
	}
	return nil
}

// extractVideosFromInitialData recursively walks ytInitialData JSON for videoRenderer entries.
func extractVideosFromInitialData(data []byte, limit int) []engine.YouTubeVideo {
	var results []engine.YouTubeVideo
	var walk func(v json.RawMessage)
	walk = func(v json.RawMessage) {
		if len(results) >= limit {
			return
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(v, &obj); err == nil {
			if raw, ok := obj["videoRenderer"]; ok {
				var vr ytSearchResult
				if err := json.Unmarshal(raw, &vr.VideoRenderer); err == nil &&
					vr.VideoRenderer != nil && vr.VideoRenderer.VideoID != "" {
					title := ""
					if len(vr.VideoRenderer.Title.Runs) > 0 {
						title = vr.VideoRenderer.Title.Runs[0].Text
					}
					channel := ""
					if len(vr.VideoRenderer.OwnerText.Runs) > 0 {
						channel = vr.VideoRenderer.OwnerText.Runs[0].Text
					}
					var snippetParts []string
					if vr.VideoRenderer.DescriptionSnippet != nil {
						for _, r := range vr.VideoRenderer.DescriptionSnippet.Runs {
							snippetParts = append(snippetParts, r.Text)
						}
					}
					results = append(results, engine.YouTubeVideo{
						ID:      vr.VideoRenderer.VideoID,
						Title:   title,
						URL:     "https://www.youtube.com/watch?v=" + vr.VideoRenderer.VideoID,
						Snippet: engine.Truncate(channel+": "+strings.Join(snippetParts, ""), 200),
					})
					return
				}
			}
			for _, child := range obj {
				if len(results) >= limit {
					return
				}
				walk(child)
			}
			return
		}
		var arr []json.RawMessage
		if err := json.Unmarshal(v, &arr); err == nil {
			for _, item := range arr {
				if len(results) >= limit {
					return
				}
				walk(item)
			}
		}
	}
	walk(data)
	return results
}

// FetchTranscriptsParallel fetches transcripts for the top N videos concurrently.
func FetchTranscriptsParallel(ctx context.Context, videos []engine.YouTubeVideo, langs []string, limit int) []engine.YouTubeVideo {
	if limit <= 0 || limit > len(videos) {
		limit = len(videos)
	}

	fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var mu sync.Mutex
	var wg sync.WaitGroup

	for i := 0; i < limit; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			transcript, err := FetchYouTubeTranscript(fetchCtx, videos[idx].ID, langs)
			if err != nil {
				slog.Debug("youtube: transcript failed",
					slog.String("id", videos[idx].ID), slog.Any("err", err))
				return
			}
			if len(transcript) > ytTranscriptMaxLen {
				transcript = transcript[:ytTranscriptMaxLen] + "..."
			}
			mu.Lock()
			videos[idx].Transcript = transcript
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	return videos
}

// SummarizeYouTubeResults summarizes YouTube search + transcript results using the LLM.
func SummarizeYouTubeResults(ctx context.Context, query string, videos []engine.YouTubeVideo) (engine.YouTubeSearchOutput, error) {
	if len(videos) == 0 {
		return engine.YouTubeSearchOutput{Query: query, Summary: "No videos found."}, nil
	}

	results := make([]engine.SearxngResult, len(videos))
	contents := make(map[string]string, len(videos))
	for i, v := range videos {
		snippet := v.Snippet
		if snippet == "" {
			snippet = v.Title
		}
		results[i] = engine.SearxngResult{Title: v.Title, URL: v.URL, Content: snippet}
		if v.Transcript != "" {
			contents[v.URL] = v.Transcript
		}
	}

	llmOut, err := engine.SummarizeWithInstruction(ctx, query, engine.YouTubeSearchInstruction, engine.Cfg.MaxContentChars, results, contents)
	if err != nil {
		return engine.YouTubeSearchOutput{Query: query, Videos: videos, Summary: "LLM summarization failed: " + err.Error()}, nil
	}
	return engine.YouTubeSearchOutput{Query: query, Videos: videos, Summary: llmOut.Answer}, nil
}
