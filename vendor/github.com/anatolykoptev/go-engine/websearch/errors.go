package websearch

import (
	"errors"
	"fmt"
	"time"
)

// ErrRateLimited is returned when a search engine blocks the request.
type ErrRateLimited struct {
	Engine     string
	RetryAfter time.Duration
}

func (e *ErrRateLimited) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("rate limited by %s (retry after %s)", e.Engine, e.RetryAfter)
	}
	return "rate limited by " + e.Engine
}

// ErrCredentialInvalid is returned when the OAuth client credentials are rejected
// by the token endpoint (HTTP 401 on POST /api/v1/access_token).
// Distinct from a 401 on a GET request (which means the token expired, not bad creds).
var ErrCredentialInvalid = errors.New("reddit: invalid client credentials")

// ErrTransient indicates a transient server-side failure (5xx) that callers
// may treat as escalatable — lower tiers should be attempted.
var ErrTransient = errors.New("transient server error")
