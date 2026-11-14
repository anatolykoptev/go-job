package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/redact"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// YouTube search — Data API v3 with HTML-parsing fallback, parallel transcript fetching,
// and LLM summarization.

const (
	ytDataAPIBase       = "https://www.googleapis.com/youtube/v3"
	ytInitialDataMarker = "var ytInitialData = "
	ytSearchFilter      = "EgIQAQ%3D%3D" // videos-only filter param
	ytTranscriptMaxLen  = 8000           // max transcript chars sent to LLM
)

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

// --- ytInitialData parsing types ---

func doYouTubeDataSearch(ctx context.Context, query, language string, limit int, apiKey string) ([]engine.YouTubeVideo, error) {
	params := url.Values{}
	params.Set("part", "snippet")
	params.Set("q", query)
	params.Set("type", "video")
	params.Set("maxResults", strconv.Itoa(limit))
	params.Set("key", apiKey)
	if language != "" && language != engine.LangAll {
		params.Set("relevanceLanguage", language)
	}

	apiURL := ytDataAPIBase + "/search?" + params.Encode()
	resp, err := engine.RetryHTTP(ctx, engine.DefaultRetryConfig, func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", engine.UserAgentBot)
		return engine.Cfg.HTTPClient.Do(req) //nolint:gosec // intentional outbound HTTP request
	})
	if err != nil {
		// The key rides in the query string, so the *url.Error from Client.Do
		// carries it. Scrub before the error is wrapped, logged or returned.
		return nil, fmt.Errorf("youtube data API: %w", redact.Error(err, apiKey))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
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
