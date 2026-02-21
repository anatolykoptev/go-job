package sources

import (
	"github.com/anatolykoptev/go_job/internal/engine"
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// YouTube transcript fetching.
// Primary:  /next → engagement panel → /get_transcript  (works from datacenter IPs)
// Fallback: ANDROID Innertube /player → captionTracks   (works from non-blocked IPs)

// getTranscriptRE extracts the continuation token from a raw /next JSON response.
var getTranscriptRE = regexp.MustCompile(`"getTranscriptEndpoint":\{"params":"([^"]+)"`)

func extractTranscriptToken(data []byte) (string, error) {
	if m := getTranscriptRE.FindSubmatch(data); len(m) >= 2 {
		// The params value in the /next JSON response is URL-encoded.
		// /get_transcript expects the decoded (raw base64) form.
		decoded, err := url.QueryUnescape(string(m[1]))
		if err != nil {
			return string(m[1]), nil
		}
		return decoded, nil
	}
	return "", errors.New("getTranscriptEndpoint not found in engagement panels")
}

// parseTranscriptSegments extracts plain text from a /get_transcript JSON response.
func parseTranscriptSegments(resp ytGetTranscriptResp) string {
	var sb strings.Builder
	for _, action := range resp.Actions {
		if action.UpdateEngagementPanelAction == nil {
			continue
		}
		segs := action.UpdateEngagementPanelAction.Content.
			TranscriptRenderer.Content.
			TranscriptSearchPanelRenderer.Body.
			TranscriptSegmentListRenderer.InitialSegments
		for _, seg := range segs {
			if seg.TranscriptSegmentRenderer == nil {
				continue
			}
			for _, run := range seg.TranscriptSegmentRenderer.Snippet.Runs {
				if run.Text != "" {
					if sb.Len() > 0 {
						sb.WriteByte(' ')
					}
					sb.WriteString(run.Text)
				}
			}
		}
	}
	return sb.String()
}

// fetchTranscriptViaEngagementPanel fetches a transcript via:
//  1. POST /next → get engagementPanels containing transcript continuation token
//  2. POST /get_transcript with the token → JSON segments
//
// This approach works from datacenter IPs where /player returns LOGIN_REQUIRED.
func fetchTranscriptViaEngagementPanel(ctx context.Context, videoID string) (string, error) {
	visitorData := generateVisitorData()

	nextData, err := postInnerTubeWEB(ctx, ytNextURL, map[string]any{
		"videoId": videoID,
		"context": ytWebContext(visitorData),
	}, visitorData)
	if err != nil {
		return "", fmt.Errorf("/next: %w", err)
	}

	token, err := extractTranscriptToken(nextData)
	if err != nil {
		return "", fmt.Errorf("token: %w", err)
	}

	transcriptData, err := postInnerTubeWEB(ctx, ytGetTranscriptURL, map[string]any{
		"params": token,
		"context": map[string]any{
			"client": ytWebClientCtx{
				ClientName:    "WEB",
				ClientVersion: ytWebVersion,
				VisitorData:   visitorData,
				Hl:            "en",
				Gl:            "US",
			},
		},
	}, visitorData)
	if err != nil {
		return "", fmt.Errorf("/get_transcript: %w", err)
	}

	var transcriptResp ytGetTranscriptResp
	if err := json.Unmarshal(transcriptData, &transcriptResp); err != nil {
		return "", fmt.Errorf("decode transcript: %w", err)
	}

	text := parseTranscriptSegments(transcriptResp)
	if text == "" {
		return "", errors.New("empty transcript segments")
	}
	return text, nil
}

// needsPoToken reports whether a caption track URL requires a PoToken (browser-only).
// Tracks with &exp=xpe cannot be fetched server-side.
func needsPoToken(baseURL string) bool {
	return strings.Contains(baseURL, "&exp=xpe")
}

// pickBestTrack selects the best usable caption track for the given language preferences.
// Skips tracks that require PoToken — those only work in a browser.
func pickBestTrack(tracks []captionTrack, langs []string) (captionTrack, bool) {
	usable := make([]captionTrack, 0, len(tracks))
	for _, t := range tracks {
		if !needsPoToken(t.BaseURL) {
			usable = append(usable, t)
		}
	}
	if len(usable) == 0 {
		return tracks[0], false
	}
	// 1. Manual track in preferred language
	for _, lang := range langs {
		for _, t := range usable {
			if t.LanguageCode == lang && t.Kind != "asr" {
				return t, true
			}
		}
	}
	// 2. Auto-generated track in preferred language
	for _, lang := range langs {
		for _, t := range usable {
			if t.LanguageCode == lang {
				return t, true
			}
		}
	}
	// 3. Any English track
	for _, t := range usable {
		if strings.HasPrefix(t.LanguageCode, "en") {
			return t, true
		}
	}
	return usable[0], true
}

// fetchTimedText fetches and parses a YouTube timedtext XML caption URL.
func fetchTimedText(ctx context.Context, baseURL string) (string, error) {
	resp, err := engine.RetryHTTP(ctx, engine.DefaultRetryConfig, func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", engine.UserAgentBot)
		return engine.Cfg.HTTPClient.Do(req)
	})
	if err != nil {
		return "", fmt.Errorf("fetch timedtext: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		return "", err
	}

	var tt ytTimedText
	if err := xml.Unmarshal(body, &tt); err != nil {
		return "", fmt.Errorf("parse timedtext XML: %w", err)
	}

	var sb strings.Builder
	for _, line := range tt.Lines {
		text := engine.CleanHTML(line.Text)
		if text != "" {
			if sb.Len() > 0 {
				sb.WriteByte(' ')
			}
			sb.WriteString(text)
		}
	}
	return sb.String(), nil
}

// fetchTranscriptViaPlayer uses the ANDROID Innertube /player endpoint.
// Works from non-blocked (residential/cloud) IP addresses.
func fetchTranscriptViaPlayer(ctx context.Context, videoID string, langs []string) (string, error) {
	reqBody, err := json.Marshal(innertubeReq{
		VideoID: videoID,
		Context: innertubeCtx{
			Client: innertubeClient{
				ClientName:        "ANDROID",
				ClientVersion:     ytAndroidVersion,
				AndroidSdkVersion: 30,
				Hl:                "en",
				Gl:                "US",
			},
		},
		RacyCheckOk:    true,
		ContentCheckOk: true,
	})
	if err != nil {
		return "", err
	}

	resp, err := engine.RetryHTTP(ctx, engine.DefaultRetryConfig, func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, ytInnertubeURL+"?prettyPrint=false", bytes.NewReader(reqBody))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", ytAndroidUA)
		req.Header.Set("X-Youtube-Client-Name", "3")
		req.Header.Set("X-Youtube-Client-Version", ytAndroidVersion)
		return engine.Cfg.HTTPClient.Do(req)
	})
	if err != nil {
		return "", fmt.Errorf("android innertube: %w", err)
	}
	defer resp.Body.Close()

	var playerResp innertubePlayerResp
	if err := json.NewDecoder(resp.Body).Decode(&playerResp); err != nil {
		return "", fmt.Errorf("decode player: %w", err)
	}
	if playerResp.Captions == nil {
		reason := ""
		if playerResp.PlayabilityStatus != nil {
			reason = playerResp.PlayabilityStatus.Reason
		}
		if reason != "" {
			return "", fmt.Errorf("captions unavailable: %s", reason)
		}
		return "", errors.New("no captions in player response")
	}
	tracks := playerResp.Captions.PlayerCaptionsTracklistRenderer.CaptionTracks
	if len(tracks) == 0 {
		return "", errors.New("no caption tracks")
	}
	track, ok := pickBestTrack(tracks, langs)
	if !ok {
		return "", errors.New("all caption tracks require PoToken")
	}
	return fetchTimedText(ctx, track.BaseURL)
}

// ytInitialPlayerResponseMarker marks the start of the player response JSON in watch page HTML.
const ytInitialPlayerResponseMarker = "ytInitialPlayerResponse = "

// fetchTranscriptViaPageScrape scrapes the YouTube watch page HTML and extracts
// the caption track XML URL from ytInitialPlayerResponse. Works from any IP.
func fetchTranscriptViaPageScrape(ctx context.Context, videoID string, langs []string) (string, error) {
	watchURL := "https://www.youtube.com/watch?v=" + videoID

	resp, err := engine.RetryHTTP(ctx, engine.DefaultRetryConfig, func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, watchURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", engine.RandomUserAgent())
		req.Header.Set("Accept-Language", "en-US,en;q=0.9")
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
		return engine.Cfg.HTTPClient.Do(req)
	})
	if err != nil {
		return "", fmt.Errorf("watch page: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 6*1024*1024))
	if err != nil {
		return "", fmt.Errorf("read watch page: %w", err)
	}

	// Extract ytInitialPlayerResponse JSON
	idx := strings.Index(string(body), ytInitialPlayerResponseMarker)
	if idx < 0 {
		return "", errors.New("ytInitialPlayerResponse not found in watch page")
	}
	jsonData := extractJSON(body[idx+len(ytInitialPlayerResponseMarker):])
	if jsonData == nil {
		return "", errors.New("failed to extract ytInitialPlayerResponse JSON")
	}

	// Parse captions from player response
	var playerResp innertubePlayerResp
	if err := json.Unmarshal(jsonData, &playerResp); err != nil {
		return "", fmt.Errorf("decode ytInitialPlayerResponse: %w", err)
	}
	if playerResp.Captions == nil {
		return "", errors.New("no captions in ytInitialPlayerResponse")
	}
	tracks := playerResp.Captions.PlayerCaptionsTracklistRenderer.CaptionTracks
	if len(tracks) == 0 {
		return "", errors.New("no caption tracks in watch page")
	}
	track, ok := pickBestTrack(tracks, langs)
	if !ok {
		return "", errors.New("all tracks require PoToken")
	}
	return fetchTimedText(ctx, track.BaseURL)
}

// FetchYouTubeTranscript fetches the transcript for a YouTube video.
// Primary:  scrape watch page ytInitialPlayerResponse → caption XML (works from any IP)
// Fallback: engagement panel /next → /get_transcript (requires valid session)
// Fallback: ANDROID Innertube /player → captionTracks
func FetchYouTubeTranscript(ctx context.Context, videoID string, langs []string) (string, error) {
	engine.IncrYouTubeTranscript()

	if text, err := fetchTranscriptViaPageScrape(ctx, videoID, langs); err == nil {
		return text, nil
	} else {
		slog.Warn("youtube: page scrape failed, trying engagement panel",
			slog.String("id", videoID), slog.Any("err", err))
	}

	if text, err := fetchTranscriptViaEngagementPanel(ctx, videoID); err == nil {
		return text, nil
	} else {
		slog.Warn("youtube: engagement panel failed, trying player",
			slog.String("id", videoID), slog.Any("err", err))
	}

	return fetchTranscriptViaPlayer(ctx, videoID, langs)
}
