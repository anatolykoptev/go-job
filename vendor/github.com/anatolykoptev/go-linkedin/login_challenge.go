package linkedin

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	verifyURL       = baseURL + "/checkpoint/challenge/verifyV2"
	challengePoll   = 5 * time.Second
	challengeMaxDur = 90 * time.Second
)

var hiddenFieldRe = regexp.MustCompile(`<input\s+name="([^"]+)"\s+value="([^"]*)"`)

// handleChallenge follows the App Challenge flow: fetch challenge page, extract form,
// poll verifyV2 until the user approves on mobile, then follow redirects to get cookies.
func (c *Client) handleChallenge(ctx context.Context, challengeURL string, cookies map[string]string) error {
	form, err := c.fetchChallengeForm(ctx, challengeURL, cookies)
	if err != nil {
		return fmt.Errorf("challenge: %w", err)
	}

	challengeID := form.Get("challengeId")
	slog.Info("linkedin: App Challenge — approve in LinkedIn mobile app",
		"challenge_id", challengeID[:min(20, len(challengeID))])

	if c.cfg.OnChallenge != nil {
		c.cfg.OnChallenge(challengeID)
	}

	return c.pollChallenge(ctx, form, challengeURL, cookies)
}

// fetchChallengeForm GETs the challenge page and extracts hidden form fields.
func (c *Client) fetchChallengeForm(ctx context.Context, challengeURL string, cookies map[string]string) (url.Values, error) {
	headers := c.loginHeaders()
	headers["cookie"] = buildCookieString(cookies)
	headers["referer"] = loginSubmitURL

	body, respHeaders, status, err := c.bc.DoWithHeaderOrderCtx(
		ctx, "GET", challengeURL, headers, nil, loginHeaderOrder,
	)
	if err != nil {
		return nil, fmt.Errorf("fetch challenge page: %w", err)
	}
	if status != 200 {
		return nil, fmt.Errorf("fetch challenge page: status %d", status)
	}

	for k, v := range parseJoinedSetCookies(respHeaders["set-cookie"]) {
		cookies[k] = v
	}

	fields := hiddenFieldRe.FindAllSubmatch(body, -1)
	if len(fields) == 0 {
		return nil, fmt.Errorf("no hidden fields found in challenge page")
	}

	form := url.Values{}
	for _, f := range fields {
		form.Set(string(f[1]), string(f[2]))
	}
	return form, nil
}

// pollChallenge submits the challenge form repeatedly until LinkedIn responds with
// a redirect (approve succeeded) or the context/timeout expires.
func (c *Client) pollChallenge(ctx context.Context, form url.Values, referer string, cookies map[string]string) error {
	deadline := time.Now().Add(challengeMaxDur)

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return fmt.Errorf("challenge: %w", ctx.Err())
		case <-time.After(challengePoll):
		}

		headers := c.loginHeaders()
		headers["content-type"] = "application/x-www-form-urlencoded"
		headers["origin"] = baseURL
		headers["referer"] = referer
		headers["cookie"] = buildCookieString(cookies)

		_, respHeaders, status, err := c.bc.DoWithHeaderOrderCtx(
			ctx, "POST", verifyURL, headers, strings.NewReader(form.Encode()), loginHeaderOrder,
		)
		if err != nil {
			return fmt.Errorf("challenge poll: %w", err)
		}

		for k, v := range parseJoinedSetCookies(respHeaders["set-cookie"]) {
			cookies[k] = v
		}

		if status >= 300 && status < 400 {
			location := resolveURL(respHeaders["location"])
			slog.Info("linkedin: App Challenge approved, following redirect")
			return c.followChallengeRedirects(ctx, location, cookies)
		}
		// status 200 = still waiting for approve
	}

	return &ChallengeError{URL: referer, Message: "app challenge timed out (90s) — no approve received"}
}

// followChallengeRedirects follows the post-challenge redirect chain to get session cookies.
func (c *Client) followChallengeRedirects(ctx context.Context, location string, cookies map[string]string) error {
	for range maxRedirects {
		headers := c.loginHeaders()
		headers["cookie"] = buildCookieString(cookies)

		_, respHeaders, status, err := c.bc.DoWithHeaderOrderCtx(
			ctx, "GET", location, headers, nil, loginHeaderOrder,
		)
		if err != nil {
			return fmt.Errorf("challenge redirect: %w", err)
		}

		for k, v := range parseJoinedSetCookies(respHeaders["set-cookie"]) {
			cookies[k] = v
		}

		if cookies["li_at"] != "" {
			return c.applyLoginCookies(cookies)
		}

		if status >= 300 && status < 400 {
			location = resolveURL(respHeaders["location"])
			if strings.Contains(location, "/feed") {
				return c.applyLoginCookies(cookies)
			}
			continue
		}

		// Non-redirect, check cookies
		if cookies["li_at"] != "" {
			return c.applyLoginCookies(cookies)
		}
		return fmt.Errorf("%w: challenge redirect returned %d without li_at", ErrLoginFailed, status)
	}
	return fmt.Errorf("%w: too many challenge redirects", ErrLoginFailed)
}
