package llm

import (
	kitllm "github.com/anatolykoptev/go-kit/llm"
)

// ErrUnavailable is re-exported from go-kit/llm so callers can do
// errors.Is(err, llm.ErrUnavailable) against engine-level errors without
// importing kit directly. Value is identical to kitllm.ErrUnavailable.
var ErrUnavailable = kitllm.ErrUnavailable

// NewOptional returns an engine Client that gracefully no-ops when no
// API key is configured. The bool reports whether a real kit client was
// constructed under the hood. When false, all Complete* methods return
// ("", ErrUnavailable). NewOptional never returns nil.
func NewOptional(opts ...Option) (*Client, bool) {
	cfg := config{
		temperature: defaultTemperature,
		maxTokens:   defaultMaxTokens,
	}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.apiKey == "" {
		c := &Client{
			disabled:    true,
			temperature: cfg.temperature,
			maxTokens:   cfg.maxTokens,
			metrics:     cfg.metrics,
		}
		return c, false
	}
	return New(opts...), true
}
