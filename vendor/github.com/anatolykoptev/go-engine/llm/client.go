// Package llm provides an OpenAI-compatible LLM client with retry
// and API key fallback rotation.
//
// Delegates to go-kit/llm for HTTP transport, retry, and key rotation.
// Preserves the go-engine API (Complete, CompleteParams) unchanged.
package llm

import (
	"context"
	"strings"
	"time"

	kitllm "github.com/anatolykoptev/go-kit/llm"
	"github.com/anatolykoptev/go-kit/metrics"
)

const (
	defaultTemperature = 0.3
	defaultMaxTokens   = 2000
)

// Client communicates with an OpenAI-compatible LLM API.
type Client struct {
	kit         *kitllm.Client
	temperature float64
	maxTokens   int
	metrics     *metrics.Registry
	// disabled is set by NewOptional when no API key is configured.
	// When true, all Complete* methods short-circuit to ErrUnavailable.
	disabled bool
}

// Option configures a Client.
type Option func(*config)

type config struct {
	apiBase     string
	apiKey      string
	fallbacks   []string
	model       string
	modelChain  []string
	temperature float64
	maxTokens   int
	metrics     *metrics.Registry
}

// WithAPIBase sets the API base URL (e.g. "http://127.0.0.1:8317/v1").
func WithAPIBase(url string) Option {
	return func(c *config) { c.apiBase = url }
}

// WithAPIKey sets the primary API key.
func WithAPIKey(key string) Option {
	return func(c *config) { c.apiKey = key }
}

// WithAPIKeyFallbacks sets fallback API keys for quota rotation.
func WithAPIKeyFallbacks(keys []string) Option {
	return func(c *config) { c.fallbacks = keys }
}

// WithModel sets the LLM model name.
func WithModel(model string) Option {
	return func(c *config) { c.model = model }
}

// WithModelFallbackChain sets a cross-provider model fallback chain.
// При rate-limit/недоступности primary model клиент пробует следующие
// модели из chain (с одним baseURL+apiKey, разными model id).
//
// Use case: cliproxyapi на :8317 с одним CLI_PROXY_API_KEY роутит к
// gemini/cerebras/groq/openrouter по model id. Chain даёт cross-provider
// failure-domain — Google outage walk'ает к Cerebras, Cerebras 429 → Groq.
//
// Implementation: делегирует kitllm.WithEndpoints + BuildModelChainEndpoints.
// ВАЖНО: WithEndpoints в go-kit отключает rotation через WithFallbackKeys —
// либо chain моделей либо chain ключей, не оба одновременно.
//
// Pass nil или пустой slice → no-op (поведение как без option).
func WithModelFallbackChain(chain []string) Option {
	return func(c *config) { c.modelChain = chain }
}

// WithTemperature sets the default temperature.
func WithTemperature(t float64) Option {
	return func(c *config) { c.temperature = t }
}

// WithMaxTokens sets the default max tokens.
func WithMaxTokens(n int) Option {
	return func(c *config) { c.maxTokens = n }
}

// WithMetrics sets the metrics registry.
func WithMetrics(m *metrics.Registry) Option {
	return func(c *config) { c.metrics = m }
}

// New creates an LLM client.
func New(opts ...Option) *Client {
	cfg := config{
		temperature: defaultTemperature,
		maxTokens:   defaultMaxTokens,
	}
	for _, o := range opts {
		o(&cfg)
	}

	var kitOpts []kitllm.Option
	if len(cfg.modelChain) > 0 {
		// Model chain takes precedence: kit's WithEndpoints disables
		// WithFallbackKeys rotation, so the chain wins when both are set.
		eps := kitllm.BuildModelChainEndpoints(cfg.apiBase, cfg.apiKey, cfg.model, cfg.modelChain)
		kitOpts = append(kitOpts, kitllm.WithEndpoints(eps))
	} else if len(cfg.fallbacks) > 0 {
		kitOpts = append(kitOpts, kitllm.WithFallbackKeys(cfg.fallbacks))
	}

	kit := kitllm.NewClient(cfg.apiBase, cfg.apiKey, cfg.model, kitOpts...)
	return &Client{
		kit:         kit,
		temperature: cfg.temperature,
		maxTokens:   cfg.maxTokens,
		metrics:     cfg.metrics,
	}
}

// Complete sends a prompt using the configured temperature and max_tokens.
func (c *Client) Complete(ctx context.Context, prompt string) (string, error) {
	if c.disabled {
		return "", ErrUnavailable
	}
	return c.CompleteParams(ctx, prompt, c.temperature, c.maxTokens)
}

// CompleteParams sends a prompt with explicit temperature and maxTokens.
func (c *Client) CompleteParams(ctx context.Context, prompt string, temperature float64, maxTokens int) (string, error) {
	if c.disabled {
		return "", ErrUnavailable
	}
	var raw string
	err := metrics.TrackCall(c.metrics, "llm_calls_total", "llm_errors_total", func() error {
		var e error
		raw, e = c.kit.Complete(ctx, "", prompt,
			kitllm.WithChatTemperature(temperature),
			kitllm.WithChatMaxTokens(maxTokens),
		)
		return e
	})
	if err != nil {
		return "", err
	}
	return stripFences(raw), nil
}

// CompleteWithSystem sends a prompt with an explicit system message.
// Empty system string omits the system message (same as Complete).
func (c *Client) CompleteWithSystem(ctx context.Context, system, prompt string) (string, error) {
	if c.disabled {
		return "", ErrUnavailable
	}
	var raw string
	err := metrics.TrackCall(c.metrics, "llm_calls_total", "llm_errors_total", func() error {
		var e error
		raw, e = c.kit.Complete(ctx, system, prompt,
			kitllm.WithChatTemperature(c.temperature),
			kitllm.WithChatMaxTokens(c.maxTokens),
		)
		return e
	})
	if err != nil {
		return "", err
	}
	return stripFences(raw), nil
}

// stripFences removes markdown code fences from LLM output.
func stripFences(s string) string {
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

// ExtractJSON extracts a JSON object from LLM output that may be wrapped
// in markdown code fences or surrounded by text.
var ExtractJSON = kitllm.ExtractJSON

// ParseModelFallbackChain парсит CSV-список моделей (например из env
// LLM_MODEL_FALLBACK). Re-export из go-kit/llm — чтобы потребители engine
// могли импортировать только этот пакет.
var ParseModelFallbackChain = kitllm.ParseModelFallbackChain

// currentDate returns today's date in ISO 8601 format (UTC).
func currentDate() string {
	return time.Now().UTC().Format("2006-01-02")
}
