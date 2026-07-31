package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// goWowaRenderURL is the go-wowa Chrome render endpoint.
// Configurable via GOWOWA_URL env var; defaults to docker backend network name.
var goWowaRenderURL = func() string {
	if v := os.Getenv("GOWOWA_URL"); v != "" {
		return v + "/api/v1/render"
	}
	return "http://go-wowa:8906/api/v1/render"
}()

const goWowaRenderTimeout = 90 * time.Second

type goWowaRenderReq struct {
	URL string `json:"url"`
}

type goWowaRenderResp struct {
	URL  string `json:"url"`
	HTML string `json:"html"`
}

// fetchRenderedHTML calls go-wowa /api/v1/render and returns the rendered HTML
// for the given URL. Uses direct internal HTTP per CLAUDE.md (no Webshare proxy).
func fetchRenderedHTML(ctx context.Context, targetURL string) (string, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, goWowaRenderTimeout)
	defer cancel()

	bodyBytes, err := json.Marshal(goWowaRenderReq{URL: targetURL})
	if err != nil {
		return "", fmt.Errorf("gowowa render: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodPost, goWowaRenderURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("gowowa render: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", engine.UserAgentBot)

	client := engine.Cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("gowowa render: do: %w", err)
	}
	defer resp.Body.Close()

	raw, err := readLimitedBody(resp.Body, securityBodyLimit)
	if err != nil {
		return "", fmt.Errorf("gowowa render: read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gowowa render: status %d: %s", resp.StatusCode, renderTruncate(string(raw), 200))
	}

	var out goWowaRenderResp
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("gowowa render: decode: %w", err)
	}
	return out.HTML, nil
}

// renderTruncate truncates s to at most n bytes, appending "..." when cut.
func renderTruncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
