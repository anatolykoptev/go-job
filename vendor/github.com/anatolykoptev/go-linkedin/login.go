package linkedin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

var (
	// ErrLoginChallenge indicates LinkedIn requires additional verification (2FA, email).
	ErrLoginChallenge = errors.New("linkedin: login challenge required")

	// ErrLoginFailed indicates login failed (bad credentials or unknown error).
	ErrLoginFailed = errors.New("linkedin: login failed")
)

// ChallengeError wraps ErrLoginChallenge with the challenge URL.
type ChallengeError struct {
	URL     string
	Message string
}

func (e *ChallengeError) Error() string {
	return fmt.Sprintf("linkedin: login challenge required: %s (url: %s)", e.Message, e.URL)
}

func (e *ChallengeError) Unwrap() error { return ErrLoginChallenge }

const (
	loginPageURL   = baseURL + "/login"
	loginSubmitURL = baseURL + "/checkpoint/lg/login-submit"
	maxRedirects   = 10
)

// loginHeaderOrder is the header order for login page requests (browser-like navigation).
var loginHeaderOrder = []string{
	"sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-platform",
	"user-agent", "accept", "accept-language",
	"content-type", "origin", "referer",
	"sec-fetch-site", "sec-fetch-mode", "sec-fetch-dest",
	"cookie",
}

// Login authenticates via LinkedIn's web login flow and stores session cookies.
// It does NOT use the rate limiter — this is a one-time operation.
func (c *Client) Login(ctx context.Context, email, password string) error {
	csrf, cookies, err := c.fetchLoginPage(ctx)
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}
	if err := c.submitLogin(ctx, email, password, csrf, cookies); err != nil {
		return fmt.Errorf("login: %w", err)
	}
	return nil
}

// fetchLoginPage GETs /login and extracts CSRF token + initial cookies.
func (c *Client) fetchLoginPage(ctx context.Context) (string, map[string]string, error) {
	headers := c.loginHeaders()
	headers["accept"] = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"
	headers["sec-fetch-dest"] = "document"
	headers["sec-fetch-mode"] = "navigate"

	body, respHeaders, status, err := c.bc.DoWithHeaderOrderCtx(
		ctx, "GET", loginPageURL, headers, nil, loginHeaderOrder,
	)
	if err != nil {
		return "", nil, fmt.Errorf("fetch login page: %w", err)
	}
	if status != 200 {
		return "", nil, fmt.Errorf("fetch login page: status %d", status)
	}

	csrf := parseCSRFToken(body)
	if csrf == "" {
		return "", nil, fmt.Errorf("CSRF token not found in login page")
	}

	cookies := parseJoinedSetCookies(respHeaders["set-cookie"])
	return csrf, cookies, nil
}

// submitLogin POSTs credentials and follows redirects to determine login outcome.
func (c *Client) submitLogin(
	ctx context.Context, email, password, csrf string, cookies map[string]string,
) error {
	form := url.Values{
		"session_key":      {email},
		"session_password": {password},
		"loginCsrfParam":   {csrf},
	}

	headers := c.loginHeaders()
	headers["content-type"] = "application/x-www-form-urlencoded"
	headers["accept"] = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"
	headers["origin"] = baseURL
	headers["referer"] = loginPageURL
	headers["sec-fetch-dest"] = "document"
	headers["sec-fetch-mode"] = "navigate"
	headers["cookie"] = buildCookieString(cookies)

	currentURL := loginSubmitURL
	method := "POST"
	var bodyReader *bytes.Reader = bytes.NewReader([]byte(form.Encode()))

	for i := range maxRedirects {
		_, respHeaders, status, err := c.bc.DoWithHeaderOrderCtx(
			ctx, method, currentURL, headers, bodyReader, loginHeaderOrder,
		)
		if err != nil {
			return fmt.Errorf("login request (redirect %d): %w", i, err)
		}

		// Accumulate cookies from every response.
		for k, v := range parseJoinedSetCookies(respHeaders["set-cookie"]) {
			cookies[k] = v
		}

		if status >= 300 && status < 400 {
			location := respHeaders["location"]
			if location == "" {
				return fmt.Errorf("redirect %d with no Location header", i)
			}
			location = resolveURL(location)

			if strings.Contains(location, "/feed") {
				return c.applyLoginCookies(cookies)
			}
			if strings.Contains(location, "/checkpoint/challenge") {
				return c.handleChallenge(ctx, location, cookies)
			}

			// Follow redirect as GET with no body.
			currentURL = location
			method = "GET"
			bodyReader = nil
			headers["cookie"] = buildCookieString(cookies)
			delete(headers, "content-type")
			delete(headers, "origin")
			headers["referer"] = loginSubmitURL
			continue
		}

		// Non-redirect response — check for success cookies or fail.
		if cookies["li_at"] != "" {
			return c.applyLoginCookies(cookies)
		}
		return fmt.Errorf("%w: status %d", ErrLoginFailed, status)
	}
	return fmt.Errorf("%w: too many redirects", ErrLoginFailed)
}

func (c *Client) applyLoginCookies(cookies map[string]string) error {
	if cookies["li_at"] == "" {
		return fmt.Errorf("%w: li_at cookie not received", ErrLoginFailed)
	}
	if c.cookies == nil {
		c.cookies = make(map[string]string, len(cookies))
	}
	for k, v := range cookies {
		c.cookies[k] = v
	}
	return nil
}

func (c *Client) loginHeaders() map[string]string {
	ua := c.cfg.UserAgent
	if ua == "" {
		ua = defaultUserAgent
	}
	secChUA := c.cfg.SecChUA
	if secChUA == "" {
		secChUA = defaultSecChUA
	}
	return map[string]string{
		"user-agent":         ua,
		"accept-language":    "en-US,en;q=0.9",
		"sec-ch-ua":          secChUA,
		"sec-ch-ua-mobile":   "?0",
		"sec-ch-ua-platform": `"macOS"`,
		"sec-fetch-site":     "same-origin",
	}
}
