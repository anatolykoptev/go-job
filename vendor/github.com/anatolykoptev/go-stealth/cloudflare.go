package stealth

import (
	"fmt"
	"strings"
)

// ChallengeType identifies the kind of Cloudflare challenge.
type ChallengeType string

const (
	ChallengeJS        ChallengeType = "js_challenge"
	ChallengeTurnstile ChallengeType = "managed_challenge"
	ChallengeBlock     ChallengeType = "block"
)

// CloudflareError indicates a Cloudflare challenge or block was detected.
type CloudflareError struct {
	Type       ChallengeType
	StatusCode int
	RayID      string
}

func (e *CloudflareError) Error() string {
	return fmt.Sprintf("cloudflare %s (HTTP %d, ray %s)", e.Type, e.StatusCode, e.RayID)
}

// DetectCloudflare inspects a Response for Cloudflare challenge markers.
// Returns nil if the response is not a Cloudflare challenge.
func DetectCloudflare(resp *Response) *CloudflareError {
	if resp.StatusCode != 403 && resp.StatusCode != 503 {
		return nil
	}
	server := strings.ToLower(resp.Headers["server"])
	if !strings.Contains(server, "cloudflare") {
		return nil
	}

	body := strings.ToLower(string(resp.Body))
	rayID := resp.Headers["cf-ray"]

	if resp.StatusCode == 503 && strings.Contains(body, "challenge-platform") {
		return &CloudflareError{Type: ChallengeJS, StatusCode: resp.StatusCode, RayID: rayID}
	}

	if strings.Contains(body, "turnstile-wrapper") || strings.Contains(body, "cf-turnstile") {
		return &CloudflareError{Type: ChallengeTurnstile, StatusCode: resp.StatusCode, RayID: rayID}
	}

	if strings.Contains(body, "you have been blocked") || strings.Contains(body, "cf-error-details") {
		return &CloudflareError{Type: ChallengeBlock, StatusCode: resp.StatusCode, RayID: rayID}
	}

	return nil
}

// CloudflareDetectMiddleware inspects responses for Cloudflare challenges.
// If a challenge is detected, it returns a *CloudflareError (use errors.As to extract).
// Non-challenge responses pass through unchanged.
func CloudflareDetectMiddleware(next Handler) Handler {
	return func(req *Request) (*Response, error) {
		resp, err := next(req)
		if err != nil {
			return resp, err
		}
		if cfErr := DetectCloudflare(resp); cfErr != nil {
			return resp, cfErr
		}
		return resp, nil
	}
}

// CookieProvider obtains Cloudflare clearance cookies from an external source.
type CookieProvider interface {
	// GetCookie returns a cached cf_clearance cookie for the domain.
	// Returns empty string if no cached cookie is available.
	GetCookie(domain string) string

	// Solve attempts to solve a Cloudflare challenge and returns the cookie string.
	// The cookie string should be in "cf_clearance=value" format.
	Solve(domain string, challenge *CloudflareError) (string, error)
}

// CloudflareCookieMiddleware returns a middleware that:
//  1. Injects cached cf_clearance cookies from the provider before each request.
//  2. On Cloudflare challenge response, calls provider.Solve() to get a cookie and retries once.
func CloudflareCookieMiddleware(provider CookieProvider) Middleware {
	return func(next Handler) Handler {
		return func(req *Request) (*Response, error) {
			domain := extractDomain(req.URL)

			if cookie := provider.GetCookie(domain); cookie != "" {
				if req.Headers == nil {
					req.Headers = make(map[string]string)
				}
				req.Headers["cookie"] = appendCookie(req.Headers["cookie"], cookie)
			}

			resp, err := next(req)
			if err != nil {
				return resp, err
			}

			cfErr := DetectCloudflare(resp)
			if cfErr == nil {
				return resp, nil
			}

			cookie, solveErr := provider.Solve(domain, cfErr)
			if solveErr != nil {
				return resp, fmt.Errorf("%w: solve failed: %w", cfErr, solveErr)
			}
			if cookie == "" {
				return resp, cfErr
			}

			if req.Headers == nil {
				req.Headers = make(map[string]string)
			}
			req.Headers["cookie"] = cookie
			return next(req)
		}
	}
}

// extractDomain extracts the hostname from a URL string.
func extractDomain(rawURL string) string {
	idx := strings.Index(rawURL, "://")
	if idx < 0 {
		return rawURL
	}
	host := rawURL[idx+3:]
	if i := strings.IndexAny(host, ":/"); i >= 0 {
		host = host[:i]
	}
	return host
}

// appendCookie appends a cookie to an existing cookie header value.
func appendCookie(existing, newCookie string) string {
	if existing == "" {
		return newCookie
	}
	return existing + "; " + newCookie
}
