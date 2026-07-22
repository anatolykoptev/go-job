package linkedin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// wowaMaxBodyBytes caps the go-wowa response body read. Voyager responses are
// JSON profiles/jobs — 8MB is generous; a runaway page (e.g. an HTML dump from
// a misconfigured evaluate) is bounded.
const wowaMaxBodyBytes = 8 << 20

// wowaInteractTimeout caps a single go-wowa interact call (evaluate or
// navigate+evaluate). The in-page fetch itself is fast once CF is cleared;
// the navigate retry can take longer to settle.
const wowaInteractTimeout = 60 * time.Second

// wowaTransport is a thin net/http client for go-wowa's /api/v1/chrome/interact
// endpoint. It carries the base URL and the soft-auth secret. No go-browser /
// go-rod dependency — talk to go-wowa over plain HTTP+JSON only.
type wowaTransport struct {
	hc     *http.Client
	base   string
	secret string
}

func newWowaTransport(base, secret string) *wowaTransport {
	return &wowaTransport{
		hc:     &http.Client{Timeout: wowaInteractTimeout},
		base:   strings.TrimRight(base, "/"),
		secret: secret,
	}
}

// wowaAction is a single go-wowa interact action (evaluate / navigate).
type wowaAction struct {
	Type   string `json:"type"`
	Script string `json:"script,omitempty"`
	URL    string `json:"url,omitempty"`
}

// wowaInteractRequest is the POST body for /api/v1/chrome/interact.
type wowaInteractRequest struct {
	URL     string       `json:"url"`
	Mode    string       `json:"mode"`
	Session string       `json:"session"`
	Actions []wowaAction `json:"actions"`
}

// wowaActionResult mirrors browser.ActionResult — only the fields doCDP reads.
type wowaActionResult struct {
	Action string          `json:"action"`
	Ok     bool            `json:"ok"`
	Data   json.RawMessage `json:"data,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// wowaInteractResponse mirrors browser.InteractResponse (as returned by
// go-wowa's HandleInteract — the InteractWithProtection envelope embeds it
// and the protection/intelligence fields are omitted when nil). doCDP reads
// only Status and Actions.
type wowaInteractResponse struct {
	URL     string             `json:"url"`
	Status  string             `json:"status"` // "ok" or "error"
	Actions []wowaActionResult `json:"actions"`
	Error   string             `json:"error,omitempty"`
}

// fetchResult is the JS-side return value of the in-page fetch IIFE.
// go-wowa's doEvaluate JSON.parse's the evaluate return, so Data arrives as
// a JSON object with these keys.
type fetchResult struct {
	Redirected bool   `json:"redirected"`
	Status     int    `json:"status"`
	Body       string `json:"body"`
}

// doCDP routes a Voyager GET through go-wowa's evaluate seam: the in-page
// fetch runs same-origin from a /feed/-pinned tab, so it inherits the
// browser's CF-cleared state and genuine TLS fingerprint. Returns the same
// ([]byte, error) contract as do() so all callers are unchanged.
//
// Classification order is load-bearing: a 302 / opaqueredirect / status=0 is
// detected BEFORE any body read (a followed redirect = the infinite CF loop).
// On a redirect, ONE on-demand re-nav retry (navigate to /feed/ then evaluate)
// refreshes CF clearance; a second redirect is a hard 302 error.
func (c *Client) doCDP(ctx context.Context, endpoint string) ([]byte, error) {
	version, err := c.clientVersion(ctx)
	if err != nil {
		return nil, fmt.Errorf("linkedin: clientVersion unavailable: %w", err)
	}
	track, err := buildLiTrack(version)
	if err != nil {
		return nil, fmt.Errorf("linkedin: buildLiTrack: %w", err)
	}
	js, err := buildFetchScript(endpoint, track)
	if err != nil {
		return nil, fmt.Errorf("linkedin: build fetch script: %w", err)
	}

	res, err := c.wowa.interact(ctx, c.cfg.Session, []wowaAction{{Type: "evaluate", Script: js}})
	if err != nil {
		return nil, fmt.Errorf("linkedin: go-wowa interact: %w", err)
	}
	fr, err := parseFetchResult(res)
	if err != nil {
		return nil, fmt.Errorf("linkedin: parse go-wowa result: %w", err)
	}

	if fr.Redirected || fr.Status == 302 || fr.Status == 0 {
		// On-demand re-nav retry: navigate to /feed/ to refresh CF clearance,
		// then re-evaluate. One retry only — a second redirect is a hard 302.
		retryRes, rerr := c.wowa.interact(ctx, c.cfg.Session, []wowaAction{
			{Type: "navigate", URL: baseURL + "/feed/"},
			{Type: "evaluate", Script: js},
		})
		if rerr != nil {
			// A transport failure on the retry leg is a go-wowa problem, NOT a
			// LinkedIn redirect — surface it as a wrapped error so it is not
			// misclassified as a 302 (which downstream treats as block->rotate).
			return nil, fmt.Errorf("linkedin: go-wowa interact (retry): %w", rerr)
		}
		retryFR, perr := parseFetchResult(retryRes)
		if perr != nil {
			return nil, fmt.Errorf("linkedin: parse go-wowa result (retry): %w", perr)
		}
		if retryFR.Redirected || retryFR.Status == 302 || retryFR.Status == 0 {
			return nil, &VoyagerStatusError{Endpoint: endpoint, Status: 302}
		}
		fr = retryFR
	}

	if fr.Status == 200 && len(fr.Body) > 0 && fr.Body[0] == '<' {
		return nil, &VoyagerHTMLResponseError{Endpoint: endpoint}
	}
	if fr.Status != 200 {
		return nil, &VoyagerStatusError{Endpoint: endpoint, Status: fr.Status}
	}
	return []byte(fr.Body), nil
}

// interact POSTs to go-wowa's /api/v1/chrome/interact with the given actions
// and returns the last action's result data (the evaluate return value).
func (w *wowaTransport) interact(ctx context.Context, session string, actions []wowaAction) (json.RawMessage, error) {
	body := wowaInteractRequest{
		URL:     baseURL + "/feed/",
		Mode:    "default",
		Session: session,
		Actions: actions,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal interact request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.base+"/api/v1/chrome/interact", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build interact request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if w.secret != "" {
		req.Header.Set("X-Internal-Secret", w.secret)
	}
	resp, err := w.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("interact HTTP: %w", err)
	}
	defer resp.Body.Close()
	limited := io.LimitReader(resp.Body, wowaMaxBodyBytes)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read interact response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("go-wowa status %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	var ir wowaInteractResponse
	if err := json.Unmarshal(raw, &ir); err != nil {
		return nil, fmt.Errorf("unmarshal interact response: %w", err)
	}
	if ir.Status != "ok" {
		return nil, fmt.Errorf("go-wowa interact status %q: %s", ir.Status, ir.Error)
	}
	if len(ir.Actions) == 0 {
		return nil, fmt.Errorf("go-wowa returned no actions")
	}
	last := ir.Actions[len(ir.Actions)-1]
	if !last.Ok {
		return nil, fmt.Errorf("go-wowa action %q failed: %s", last.Action, last.Error)
	}
	return last.Data, nil
}

// parseFetchResult decodes the evaluate action's data (a JSON object produced
// by the in-page fetch IIFE) into a fetchResult.
func parseFetchResult(data json.RawMessage) (fetchResult, error) {
	var fr fetchResult
	if len(data) == 0 {
		return fr, fmt.Errorf("empty data")
	}
	if err := json.Unmarshal(data, &fr); err != nil {
		return fr, fmt.Errorf("unmarshal fetchResult: %w", err)
	}
	return fr, nil
}

// buildFetchScript constructs the in-page fetch JS. The endpoint and track
// are interpolated via json.Marshal of the Go string, producing a safe JS
// string literal — never string-concat raw (JS injection). CSRF is derived
// INSIDE the page from document.cookie (live/authoritative), not from
// c.cookies (stale). redirect:"manual" prevents the browser from following
// the CF 302-to-self loop.
func buildFetchScript(endpoint, track string) (string, error) {
	epJSON, err := json.Marshal(endpoint)
	if err != nil {
		return "", fmt.Errorf("marshal endpoint: %w", err)
	}
	trackJSON, err := json.Marshal(track)
	if err != nil {
		return "", fmt.Errorf("marshal track: %w", err)
	}
	return `(async () => {
  const m = document.cookie.match(/JSESSIONID="?([^";]+)"?/);
  const csrf = m ? m[1] : "";
  const r = await fetch(` + string(epJSON) + `, {method:"GET",
    headers:{"accept":"application/vnd.linkedin.normalized+json+2.1","csrf-token":csrf,
             "x-restli-protocol-version":"2.0.0","x-li-lang":"en_US","x-li-track":` + string(trackJSON) + `},
    credentials:"include", redirect:"manual"});
  if (r.type==="opaqueredirect" || r.status===0) return {redirected:true, status:302};
  const body = await r.text();
  return {status:r.status, body:body};
})()`, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
