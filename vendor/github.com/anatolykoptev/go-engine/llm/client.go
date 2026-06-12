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
	apiBase           string
	apiKey            string
	fallbacks         []string
	model             string
	modelChain        []string
	chainObserver     kitllm.EndpointAttemptObserver
	filterObserver    kitllm.ModelFilterObserver
	perAttemptTimeout time.Duration
	temperature       float64
	maxTokens         int
	metrics           *metrics.Registry
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

// EndpointAttemptObserver — re-export типа из go-kit/llm чтобы consumers
// могли импортировать только engine package.
type EndpointAttemptObserver = kitllm.EndpointAttemptObserver

// Endpoint — re-export типа из go-kit/llm для observer parameter.
type Endpoint = kitllm.Endpoint

// WithModelChainObserver регистрирует callback который fires once per
// endpoint attempt в chain (success или failure). Endpoint.Model несёт
// model id — caller обновляет per-model metric без middleware overhead.
//
// Работает только в паре с WithModelFallbackChain (без chain нет events).
//
//	c := engllm.New(
//	    engllm.WithAPIBase(...), engllm.WithAPIKey(...), engllm.WithModel(...),
//	    engllm.WithModelFallbackChain(chain),
//	    engllm.WithModelChainObserver(func(ep engllm.Endpoint, err error) {
//	        if err != nil { IncrModelFail(ep.Model) }
//	    }),
//	)
func WithModelChainObserver(obs EndpointAttemptObserver) Option {
	return func(c *config) { c.chainObserver = obs }
}

// WithModelFilterObserver registers a callback fired once per New() call when
// a model-chain is configured. It receives a ModelFilterEvent describing which
// models (if any) were dropped because they were absent from the live
// /v1/models set, and whether filtering degraded to the full chain.
//
// Use to emit a Prometheus counter / structured log on "N models dropped":
//
//	c := engllm.New(
//	    engllm.WithAPIBase(...), engllm.WithAPIKey(...), engllm.WithModel(...),
//	    engllm.WithModelFallbackChain(chain),
//	    engllm.WithModelFilterObserver(func(ev engllm.ModelFilterEvent) {
//	        for _, m := range ev.Dropped {
//	            IncrModelAbsent(m) // your Prometheus counter
//	        }
//	    }),
//	)
//
// nil is safe — filtering runs, observer is skipped.
// Only meaningful together with WithModelFallbackChain.
func WithModelFilterObserver(obs ModelFilterObserver) Option {
	return func(c *config) { c.filterObserver = obs }
}

// ModelFilterObserver — re-export of go-kit/llm.ModelFilterObserver so
// consumers can import only the engine package.
type ModelFilterObserver = kitllm.ModelFilterObserver

// ModelFilterEvent — re-export of go-kit/llm.ModelFilterEvent.
type ModelFilterEvent = kitllm.ModelFilterEvent

// WithPerAttemptTimeout bounds each model attempt in the fallback chain by its
// own deadline (derived from the caller's ctx). Only meaningful together with
// WithModelFallbackChain. d<=0 = no per-attempt bound (default). Delegates to
// go-kit's transport-level WithPerAttemptTimeout — the single source of truth
// for per-attempt failover timing. One slow model can no longer starve the rest
// of the chain.
func WithPerAttemptTimeout(d time.Duration) Option {
	return func(c *config) { c.perAttemptTimeout = d }
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
		//
		// Use the health-aware variant: probes {baseURL}/v1/models once at
		// construction (5-min TTL cache), drops absent models before building
		// the chain. Graceful degradation — registry nil / fetch failed / empty /
		// all-filtered → falls back to the full unfiltered chain (same as the
		// static builder). Never a new failure mode.
		reg := kitllm.NewModelRegistry()
		eps := kitllm.BuildModelChainEndpointsFiltered(
			context.Background(), reg,
			cfg.apiBase, cfg.apiKey, cfg.model, cfg.modelChain,
			cfg.filterObserver,
		)
		kitOpts = append(kitOpts, kitllm.WithEndpoints(eps))
		if cfg.chainObserver != nil {
			kitOpts = append(kitOpts, kitllm.WithEndpointAttemptObserver(cfg.chainObserver))
		}
		if cfg.perAttemptTimeout > 0 {
			kitOpts = append(kitOpts, kitllm.WithPerAttemptTimeout(cfg.perAttemptTimeout))
		}
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
// Variadic opts pass through to kit.Complete (e.g. WithChatModel for per-call
// model override). Most callers use no opts.
func (c *Client) Complete(ctx context.Context, prompt string, opts ...ChatOption) (string, error) {
	if c.disabled {
		return "", ErrUnavailable
	}
	return c.CompleteParams(ctx, prompt, c.temperature, c.maxTokens, opts...)
}

// CompleteParams sends a prompt with explicit temperature and maxTokens.
// Variadic opts pass through to kit.Complete after temperature/maxTokens
// (later opts override earlier in chatConfig.apply order).
func (c *Client) CompleteParams(ctx context.Context, prompt string, temperature float64, maxTokens int, opts ...ChatOption) (string, error) {
	if c.disabled {
		return "", ErrUnavailable
	}
	var raw string
	err := metrics.TrackCall(c.metrics, "llm_calls_total", "llm_errors_total", func() error {
		var e error
		kitOpts := append([]ChatOption{
			kitllm.WithChatTemperature(temperature),
			kitllm.WithChatMaxTokens(maxTokens),
		}, opts...)
		raw, e = c.kit.Complete(ctx, "", prompt, kitOpts...)
		return e
	})
	if err != nil {
		return "", err
	}
	return stripFences(raw), nil
}

// CompleteWithSystem sends a prompt with an explicit system message.
// Empty system string omits the system message (same as Complete).
// Variadic opts pass through to kit.Complete.
func (c *Client) CompleteWithSystem(ctx context.Context, system, prompt string, opts ...ChatOption) (string, error) {
	if c.disabled {
		return "", ErrUnavailable
	}
	var raw string
	err := metrics.TrackCall(c.metrics, "llm_calls_total", "llm_errors_total", func() error {
		var e error
		kitOpts := append([]ChatOption{
			kitllm.WithChatTemperature(c.temperature),
			kitllm.WithChatMaxTokens(c.maxTokens),
		}, opts...)
		raw, e = c.kit.Complete(ctx, system, prompt, kitOpts...)
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

// ChatOption — re-export типа из go-kit/llm для per-call request options.
type ChatOption = kitllm.ChatOption

// WithChatModel — re-export. Per-call model override (empty string = no
// override). Use case: one-off model substitution within a single call.
// NOTE: the per-attempt timeout chain loop pattern (caller iterates models
// with context.WithTimeout + WithChatModel per call) is deprecated in favour
// of WithModelFallbackChain + WithPerAttemptTimeout, which handles per-attempt
// failover at the transport level. WithChatModel is kept for go-wowa and other
// callers that perform per-call model substitution unrelated to failover.
var WithChatModel = kitllm.WithChatModel

// WithChatTemperature — re-export per-call temperature override.
var WithChatTemperature = kitllm.WithChatTemperature

// WithChatMaxTokens — re-export per-call max tokens override.
var WithChatMaxTokens = kitllm.WithChatMaxTokens

// currentDate returns today's date in ISO 8601 format (UTC).
func currentDate() string {
	return time.Now().UTC().Format("2006-01-02")
}
