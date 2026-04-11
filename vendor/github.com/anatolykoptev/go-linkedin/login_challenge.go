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

// handleChallenge detects the challenge type (App Challenge vs Email PIN) and handles it.
func (c *Client) handleChallenge(ctx context.Context, challengeURL string, cookies map[string]string) error {
	form, err := c.fetchChallengeForm(ctx, challengeURL, cookies)
	if err != nil {
		return fmt.Errorf("challenge: %w", err)
	}

	pageInstance := form.Get("pageInstance")
	challengeID := form.Get("challengeId")

	if strings.Contains(pageInstance, "emailPin") {
		return c.handleEmailPinChallenge(ctx, form, challengeURL, cookies)
	}

	// Default: App Challenge (mobile approve)
	slog.Info("linkedin: App Challenge — approve in LinkedIn mobile app",
		"challenge_id", challengeID[:min(20, len(challengeID))])
	if c.cfg.OnChallenge != nil {
		c.cfg.OnChallenge(challengeID)
	}
	return c.pollChallenge(ctx, form, challengeURL, cookies)
}

// handleEmailPinChallenge requests a PIN from the user and submits it.
func (c *Client) handleEmailPinChallenge(ctx context.Context, form url.Values, referer string, cookies map[string]string) error {
	email := "" // LinkedIn doesn't put email in form; use config or empty
	for _, v := range c.cookies {
		if strings.Contains(v, "@") {
			email = v
			break
		}
	}

	slog.Info("linkedin: Email PIN challenge — check your email", "email", email)

	if c.cfg.OnEmailPin == nil {
		return &ChallengeError{
			URL:     referer,
			Message: fmt.Sprintf("email PIN required for %s — set OnEmailPin callback", email),
		}
	}

	pin, err := c.cfg.OnEmailPin(email)
	if err != nil {
		return fmt.Errorf("get email PIN: %w", err)
	}
	if pin == "" {
		return &ChallengeError{URL: referer, Message: "empty PIN provided"}
	}

	form.Set("pin", pin)
	return c.submitChallenge(ctx, form, referer, cookies)
}

// submitChallenge POSTs the challenge form once and follows redirects.
func (c *Client) submitChallenge(ctx context.Context, form url.Values, referer string, cookies map[string]string) error {
	headers := c.loginHeaders()
	headers["content-type"] = "application/x-www-form-urlencoded"
	headers["origin"] = baseURL
	headers["referer"] = referer
	headers["cookie"] = buildCookieString(cookies)

	body, respHeaders, status, err := c.bc.DoWithHeaderOrderCtx(
		ctx, "POST", verifyURL, headers, strings.NewReader(form.Encode()), loginHeaderOrder,
	)
	if err != nil {
		return fmt.Errorf("submit challenge: %w", err)
	}

	for k, v := range parseJoinedSetCookies(respHeaders["set-cookie"]) {
		cookies[k] = v
	}

	slog.Info("linkedin: PIN submit response", "status", status,
		"location", respHeaders["location"],
		"has_li_at", cookies["li_at"] != "",
		"body_len", len(body))

	if status >= 300 && status < 400 {
		location := resolveURL(respHeaders["location"])
		return c.followChallengeRedirects(ctx, location, cookies)
	}

	if cookies["li_at"] != "" {
		return c.applyLoginCookies(cookies)
	}

	// Check if response is another challenge or error page
	bodyStr := string(body)
	if strings.Contains(bodyStr, "incorrect") || strings.Contains(bodyStr, "expired") {
		return fmt.Errorf("%w: PIN rejected or expired", ErrLoginFailed)
	}

	// Maybe success page with li_at in Set-Cookie but not parsed
	return fmt.Errorf("%w: no li_at after PIN submit (status %d)", ErrLoginFailed, status)
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

// pollChallenge submits the App Challenge form repeatedly until mobile approve.
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
	}

	return &ChallengeError{URL: referer, Message: "app challenge timed out (90s)"}
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

		if cookies["li_at"] != "" {
			return c.applyLoginCookies(cookies)
		}
		return fmt.Errorf("%w: challenge redirect returned %d without li_at", ErrLoginFailed, status)
	}
	return fmt.Errorf("%w: too many challenge redirects", ErrLoginFailed)
}
