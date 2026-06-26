// Package llm provides an OpenAI-compatible LLM client with retry
// and API key fallback rotation.
//
// Delegates to go-kit/llm for HTTP transport, retry, and key rotation.
// Preserves the go-engine API (Complete, CompleteParams) unchanged.
package llm

import (
	"context"
	"os"
	"strconv"
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
	// primaryModel is the model name configured at construction time.
	// Stored separately because go-kit's Client.model is private.
	primaryModel string
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
	cooldownObserver  func(model string, cooling bool, d time.Duration)
	cooldownDuration  time.Duration
	perAttemptTimeout time.Duration
	temperature       float64
	maxTokens         int
	metrics           *metrics.Registry
	reasoningEffortModels []string
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

// CooldownConfig — re-export of go-kit/llm.CooldownConfig so consumers need
// only import the engine package.
type CooldownConfig = kitllm.CooldownConfig

// WithModelCooldownObserver registers an optional callback fired once on
// cooldown ENTRY (cooling=true, d = the cooldown duration) and once on RECOVERY
// (cooling=false, d = 0) per model in the fallback chain.
//
// Use this to emit a structured log or Prometheus counter when the primary model
// becomes quota-exhausted and the chain degrades to a fallback:
//
//	c := engllm.New(
//	    engllm.WithAPIBase(...), engllm.WithAPIKey(...), engllm.WithModel(...),
//	    engllm.WithModelFallbackChain(chain),
//	    engllm.WithModelCooldownObserver(func(model string, cooling bool, d time.Duration) {
//	        if cooling {
//	            slog.Warn("model entering cooldown", "model", model, "for", d)
//	            IncrModelCooldown(model) // your Prometheus counter
//	        }
//	    }),
//	)
//
// The callback must not block or panic (fires inside the request path on a
// state transition). nil is safe and is a no-op.
// Only meaningful together with WithModelFallbackChain.
func WithModelCooldownObserver(fn func(model string, cooling bool, d time.Duration)) Option {
	return func(c *config) { c.cooldownObserver = fn }
}

// WithModelCooldownDuration overrides the per-model cooldown duration used when
// the upstream returns no Retry-After header (the no-header case in
// go-kit/llm.CooldownConfig.Default). Precedence: this option > env
// LLM_COOLDOWN_SECONDS > built-in default (5m).
//
// Use this when deploying go-engine consumers that exhaust a daily quota in
// minutes rather than seconds — 5m keeps the fallback chain healthy for the
// rest of the day without needing a code change.
//
// d <= 0 is a no-op (falls through to env or default).
// Only meaningful together with WithModelFallbackChain.
func WithModelCooldownDuration(d time.Duration) Option {
	return func(c *config) { c.cooldownDuration = d }
}

// defaultCooldownDuration is the go-engine–level default for the no-header
// cooldown path. go-kit's own default is 60s; we raise it to 5m because
// go-engine consumers are typically daily-quota services (cliproxyapi free
// tier) where a model that returns 429 is exhausted for hours, not seconds.
// Live deployments that want a longer floor set LLM_COOLDOWN_SECONDS.
const defaultCooldownDuration = 5 * time.Minute

// resolveCooldownDuration returns the effective cooldown duration. Precedence:
//
//  1. explicit option (d > 0)
//  2. env LLM_COOLDOWN_SECONDS (integer > 0)
//  3. built-in default (5m)
func resolveCooldownDuration(explicit time.Duration) time.Duration {
	if explicit > 0 {
		return explicit
	}
	if s := os.Getenv("LLM_COOLDOWN_SECONDS"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return defaultCooldownDuration
}

// WithPerAttemptTimeout bounds each model attempt in the fallback chain by its
// own deadline (derived from the caller's ctx). Only meaningful together with
// WithModelFallbackChain. d<=0 = no per-attempt bound (default). Delegates to
// go-kit's transport-level WithPerAttemptTimeout — the single source of truth
// for per-attempt failover timing. One slow model can no longer starve the rest
// of the chain.
func WithPerAttemptTimeout(d time.Duration) Option {
	return func(c *config) { c.perAttemptTimeout = d }
}

// WithReasoningEffortModels sets the allowlist of exact model IDs that receive
// reasoning_effort in a WithEndpoints chain. Delegates to go-kit's per-endpoint
// gating in attemptEndpoint. Empty (default) = pass-through.
//
// Safe models (exact IDs): "gonka-qwen3-235b", "cerebras-glm-4.7", "nv-deepseek-v4-flash".
// Unsafe (return HTTP 400): groq-llama-70b, or-gpt-oss-120b-free, zen-deepseek-v4-flash-free.
func WithReasoningEffortModels(models []string) Option {
	return func(c *config) { c.reasoningEffortModels = models }
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
		// Cooldown default-on: go-engine is our intermediate kit, not a public
		// library — we set policy. Default duration resolved via
		// resolveCooldownDuration (option > LLM_COOLDOWN_SECONDS > 5m).
		// FailThreshold and Max stay at kit defaults (2, 10m).
		// NOTE: Default=5m < Max=10m — Max only clamps an upstream Retry-After
		// header (separate code path); Default governs the no-header case and is
		// NOT clamped by Max (verified in go-kit cooldown.go).
		// Gated inside the chain branch because cooldown only matters with a
		// multi-model chain.
		kitOpts = append(kitOpts, kitllm.WithModelCooldown(kitllm.CooldownConfig{
			Default: resolveCooldownDuration(cfg.cooldownDuration),
		}))
		if cfg.cooldownObserver != nil {
			kitOpts = append(kitOpts, kitllm.WithModelCooldownObserver(cfg.cooldownObserver))
		}
		if cfg.chainObserver != nil {
			kitOpts = append(kitOpts, kitllm.WithEndpointAttemptObserver(cfg.chainObserver))
		}
		if cfg.perAttemptTimeout > 0 {
			kitOpts = append(kitOpts, kitllm.WithPerAttemptTimeout(cfg.perAttemptTimeout))
		}
	} else if len(cfg.fallbacks) > 0 {
		kitOpts = append(kitOpts, kitllm.WithFallbackKeys(cfg.fallbacks))
	}

	if len(cfg.reasoningEffortModels) > 0 {
		kitOpts = append(kitOpts, kitllm.WithReasoningEffortModels(cfg.reasoningEffortModels))
	}

	kit := kitllm.NewClient(cfg.apiBase, cfg.apiKey, cfg.model, kitOpts...)
	return &Client{
		kit:             kit,
		temperature:     cfg.temperature,
		maxTokens:       cfg.maxTokens,
		metrics:         cfg.metrics,
		primaryModel:    cfg.model,
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

// WithReasoningEffort — re-export. Per-call reasoning effort override.
// Valid values: "none", "low", "medium", "high". "none" disables chain-of-thought
// on supported models (cerebras-glm-4.7), freeing the full token budget for output.
// The go-kit transport layer gates per-endpoint via LLM_REASONING_EFFORT_MODELS
// allowlist; mixed chains work correctly without sending the param to unsupported endpoints.
var WithReasoningEffort = kitllm.WithReasoningEffort

// currentDate returns today's date in ISO 8601 format (UTC).
func currentDate() string {
	return time.Now().UTC().Format("2006-01-02")
}
