package websearch

import (
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
