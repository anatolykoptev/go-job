package sources

import (
	"github.com/anatolykoptev/go_job/internal/engine"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
)

// YouTube Innertube API — low-level constants, types, and HTTP primitives.
// All higher-level logic lives in youtube_transcript.go and youtube_search.go.

const (
	ytInnertubeURL     = "https://www.youtube.com/youtubei/v1/player"
	ytNextURL          = "https://www.youtube.com/youtubei/v1/next"
	ytGetTranscriptURL = "https://www.youtube.com/youtubei/v1/get_transcript"
	ytWebVersion       = "2.20250222.10.00"
	ytAndroidVersion   = "20.10.38"
	ytAndroidUA        = "com.google.android.youtube/" + ytAndroidVersion + " (Linux; U; Android 11) gzip"
)

// --- ANDROID client types (/player endpoint) ---

type innertubeReq struct {
	VideoID        string       `json:"videoId"`
	Context        innertubeCtx `json:"context"`
	RacyCheckOk    bool         `json:"racyCheckOk"`
	ContentCheckOk bool         `json:"contentCheckOk"`
}

type innertubeCtx struct {
	Client innertubeClient `json:"client"`
}

type innertubeClient struct {
	ClientName        string `json:"clientName"`
	ClientVersion     string `json:"clientVersion"`
	AndroidSdkVersion int    `json:"androidSdkVersion,omitempty"`
	Hl                string `json:"hl,omitempty"`
	Gl                string `json:"gl,omitempty"`
}

type innertubePlayerResp struct {
	Captions *struct {
		PlayerCaptionsTracklistRenderer struct {
			CaptionTracks []captionTrack `json:"captionTracks"`
		} `json:"playerCaptionsTracklistRenderer"`
	} `json:"captions"`
	PlayabilityStatus *struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	} `json:"playabilityStatus"`
}

type captionTrack struct {
	BaseURL      string `json:"baseUrl"`
	LanguageCode string `json:"languageCode"`
	Kind         string `json:"kind"` // "asr" = auto-generated
}

// --- WEB client types (/next and /get_transcript endpoints) ---

type ytWebClientCtx struct {
	ClientName    string `json:"clientName"`
	ClientVersion string `json:"clientVersion"`
	VisitorData   string `json:"visitorData,omitempty"`
	Hl            string `json:"hl,omitempty"`
	Gl            string `json:"gl,omitempty"`
}

type ytWebUser struct {
	EnableSafetyMode bool `json:"enableSafetyMode"`
}

type ytWebReqCtx struct {
	UseSsl bool `json:"useSsl"`
}

// --- Timedtext XML types ---

type ytTimedText struct {
	Lines []ytLine `xml:"text"`
}

type ytLine struct {
	Text string `xml:",chardata"`
}

// --- /get_transcript response ---

type ytGetTranscriptResp struct {
	Actions []struct {
		UpdateEngagementPanelAction *struct {
			Content struct {
				TranscriptRenderer struct {
					Content struct {
						TranscriptSearchPanelRenderer struct {
							Body struct {
								TranscriptSegmentListRenderer struct {
									InitialSegments []struct {
										TranscriptSegmentRenderer *struct {
											Snippet struct {
												Runs []struct {
													Text string `json:"text"`
												} `json:"runs"`
											} `json:"snippet"`
										} `json:"transcriptSegmentRenderer"`
									} `json:"initialSegments"`
								} `json:"transcriptSegmentListRenderer"`
							} `json:"body"`
						} `json:"transcriptSearchPanelRenderer"`
					} `json:"content"`
				} `json:"transcriptRenderer"`
			} `json:"content"`
		} `json:"updateEngagementPanelAction"`
	} `json:"actions"`
}

// generateVisitorData creates a random 11-char visitor ID for Innertube requests.
func generateVisitorData() string {
	const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	b := make([]byte, 11)
	for i := range b {
		b[i] = chars[rand.Intn(len(chars))] //nolint:gosec // non-cryptographic use
	}
	return string(b)
}

// ytWebContext builds the standard WEB client context for Innertube payloads.
func ytWebContext(visitorData string) map[string]any {
	return map[string]any{
		"client": ytWebClientCtx{
			ClientName:    "WEB",
			ClientVersion: ytWebVersion,
			VisitorData:   visitorData,
			Hl:            "en",
			Gl:            "US",
		},
		"user":    ytWebUser{EnableSafetyMode: false},
		"request": ytWebReqCtx{UseSsl: true},
	}
}

// postInnerTubeWEB POSTs to a YouTube Innertube endpoint with WEB client headers.
// Uses engine.Cfg.HTTPClient and engine.RetryHTTP for consistent retry/timeout behavior.
func postInnerTubeWEB(ctx context.Context, endpoint string, payload any, visitorData string) ([]byte, error) {
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	resp, err := engine.RetryHTTP(ctx, engine.DefaultRetryConfig, func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"?prettyPrint=false", bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "*/*")
		req.Header.Set("User-Agent", engine.UserAgentChrome)
		req.Header.Set("X-Youtube-Client-Name", "1")
		req.Header.Set("X-Youtube-Client-Version", ytWebVersion)
		req.Header.Set("X-Goog-Visitor-Id", visitorData)
		req.Header.Set("Origin", "https://www.youtube.com")
		req.Header.Set("Referer", "https://www.youtube.com/")
		return engine.Cfg.HTTPClient.Do(req)
	})
	if err != nil {
		return nil, fmt.Errorf("innertube WEB [%s]: %w", endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, snippet)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 3*1024*1024))
}
