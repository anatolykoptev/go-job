package search

import "github.com/anatolykoptev/go-engine/websearch"

// ErrRateLimited is returned when a search engine blocks the request
// due to rate limiting, CAPTCHA, or IP-based throttling.
// Delegates to websearch.ErrRateLimited.
type ErrRateLimited = websearch.ErrRateLimited
