package engine

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/anatolykoptev/go-engine/extract"
	"github.com/anatolykoptev/go-engine/fetch"
	engllm "github.com/anatolykoptev/go-engine/llm"
	"github.com/anatolykoptev/go-engine/search"
	kitmetrics "github.com/anatolykoptev/go-kit/metrics"
	linkedin "github.com/anatolykoptev/go-linkedin"
	"github.com/anatolykoptev/go-stealth/proxypool"
	twitter "github.com/anatolykoptev/go-twitter"
	"github.com/anatolykoptev/go-twitter/social"
	"github.com/prometheus/client_golang/prometheus"
)

// Config holds all engine configuration, injected from main.
type Config struct {
	SearxngURL                string
	LLMAPIKey                 string
	LLMAPIKeyFallbacks        []string
	LLMAPIBase                string
	LLMModel                  string
	LLMTemperature            float64
	LLMMaxTokens              int
	MaxFetchURLs              int
	MaxContentChars           int
	FetchTimeout              time.Duration
	GithubToken               string
	GithubSearchRepos         []string
	Context7APIKey            string
	HuggingFaceToken          string
	YouTubeAPIKey             string
	YouTubeAPIKeyFallback     string
	YouTubeTranscriptsEnabled bool
	CacheMaxEntries           int
	CacheCleanupInterval      time.Duration
	ProxyPool                 proxypool.ProxyPool // replaces BrowserClient + HTTPClient
	DirectDDG                 bool                // enable DuckDuckGo direct scraper
	DirectStartpage           bool                // enable Startpage direct scraper
	DirectBrave               bool                // enable Brave direct scraper
	DirectReddit              bool                // enable Reddit direct scraper
	DirectWikipedia           bool                // enable Wikipedia direct scraper (DIRECT_WIKIPEDIA)
	DirectMarginalia          bool                // enable Marginalia direct scraper (DIRECT_MARGINALIA)
	SearchEarlyReturnAt       int                 // SEARCH_EARLY_RETURN_AT: soft result cap; 0 = go-engine default (10)
	SearchPerSourceTimeout    time.Duration       // SEARCH_PER_SOURCE_TIMEOUT: per-source cap; 0 = go-engine default (6s)
	// FetchDirectFirst enables go-engine v1.12+ direct-first tiered fallback.
	// When true, the fetcher tries Chrome-TLS direct first and escalates to proxy
	// only on anti-bot signals (HTTP 401/403/429/503, Cloudflare/PerimeterX/DataDome
	// markers, soft-block heuristic, TLS errors). Controlled by FETCH_DIRECT_FIRST env var.
	FetchDirectFirst   bool
	IndeedAPIKey       string           // overrideable via INDEED_API_KEY env
	TwitterClient      *twitter.Client  // nil = Twitter search disabled
	SocialClient       *social.Client   // nil = go-social disabled, use local twitter
	LinkedInClient     *linkedin.Client // nil = LinkedIn tools disabled
	DatabaseURL        string           // DATABASE_URL for PostgreSQL (resume graph)
	MemDBURL           string           // MEMDB_URL for vector search
	MemDBServiceSecret string           // INTERNAL_SERVICE_SECRET for MemDB auth
	EmbedURL           string           // EMBED_URL for direct embedding server

	// Bounty search tuning.
	BountyHighConfidence float32 // cosine threshold for high-confidence tier (default 0.82)
	BountyHighConfGap    float32 // max gap from best in high-confidence tier (default 0.04)
	BountyHighConfMax    int     // max results in high-confidence tier (default 10)
	BountyMedConfMax     int     // max results in medium-confidence tier (default 3)
	BountySkillBoost     float32 // boost when query matches bounty skills (default 0.05)
	BountyMinRelevance   float32 // minimum best-score to return results (default 0.75)

	// Bounty notify (ingest-side hook — no background monitor since Phase 3).
	VaelorNotifyURL    string // VAELOR_NOTIFY_URL for sending Telegram notifications
	BountyNotifyChatID string // BOUNTY_NOTIFY_CHAT_ID (default "428660")

	// LLMModelFallback is a CSV cross-provider model fallback chain
	// (e.g. "cerebras-qwen-3-235b,groq-llama-3.3-70b"). When non-empty,
	// the LLM client tries LLMModel first, then each model in chain on
	// retryable failure. All entries share LLMAPIBase+LLMAPIKey —
	// cliproxyapi routes by model id.
	//
	// MUTEX with LLMAPIKeyFallbacks: WithModelFallbackChain (WithEndpoints
	// internally) disables key rotation. Set either, not both.
	// Env: LLM_MODEL_FALLBACK (mounted via config/llm.env).
	LLMModelFallback string

	// Computed fields — populated by Init(), not set by caller.
	HTTPClient    *http.Client   // plain HTTP client for API calls
	BrowserClient *BrowserClient // proxy browser client (nil if no proxy)
}

// Package-level go-engine instances, set by Init().
var (
	cfg           Config
	fetcherProxy  *fetch.Fetcher       // with proxy, for web pages
	fetcherDirect *fetch.Fetcher       // no proxy, for raw content + internal APIs
	extractorInst *extract.Extractor   // HTML content extraction
	searxngInst   *search.SearXNG      // SearXNG client
	llmInst       *engllm.Client       // LLM client
	reg           *kitmetrics.Registry // metrics counters (Prometheus-bridged)
	httpClient    *http.Client         // plain HTTP client for GitHub API etc.
)

// Cfg exposes the engine configuration for sub-packages (jobs, sources).
var Cfg = &cfg

// Init initializes the engine with the given configuration.
func Init(c Config) {
	cfg = c
	Cfg = &cfg

	// Metrics registry (Prometheus-bridged under "gojob" namespace).
	reg = kitmetrics.NewPrometheusRegistry("gojob")
	// Pre-configure byte-scale buckets for the oversize histogram before any
	// Observe call. Must happen at startup (before traffic) — buckets lock at
	// first Observe. The seconds-shaped default (ExponentialBuckets(0.001,2,16))
	// is useless for bytes; OversizeBytesBuckets covers 1KB–4MB log-scale.
	reg.RegisterHistogram(MetricOversizeBytes, kitmetrics.WithBuckets(OversizeBytesBuckets))

	// Fetcher with proxy (for web pages, direct scrapers).
	fetcherOpts := []fetch.Option{fetch.WithTimeout(c.FetchTimeout)}
	if c.FetchDirectFirst {
		// Direct-first tiered fallback (go-engine v1.12+, Webshare bandwidth optimization).
		// Tries Chrome-TLS BrowserClient directly first; escalates to Webshare proxy pool
		// only on anti-bot signals (401/403/429/503, CF/PerimeterX/DataDome/Akamai markers).
		// When ProxyPool is nil (FETCH_DIRECT_FIRST=direct or no WEBSHARE_* vars), operates
		// direct-only with no fallback.
		fetcherOpts = append(fetcherOpts, fetch.WithDirectFirst(true))
	}
	if c.ProxyPool != nil {
		fetcherOpts = append(fetcherOpts, fetch.WithProxyPool(c.ProxyPool))
	}
	// go-engine v1.13.0 tier router metrics — counts direct/proxy fetches,
	// block signals, escalations, sticky cache size. Exposed via existing
	// /metrics endpoint (prometheus.DefaultRegisterer).
	if pm, err := fetch.NewPromMetrics(prometheus.DefaultRegisterer); err != nil {
		slog.Warn("fetch tier metrics registration failed, running without", slog.Any("error", err))
	} else {
		fetcherOpts = append(fetcherOpts, fetch.WithMetrics(pm))
	}
	fetcherProxy = fetch.New(fetcherOpts...)

	// Fetcher without proxy (for raw content, internal APIs).
	fetcherDirect = fetch.New(fetch.WithTimeout(c.FetchTimeout))

	// HTML content extractor.
	extractorInst = extract.New(extract.WithMaxContentLen(c.MaxContentChars))

	// SearXNG client (local, no proxy needed — optional).
	if c.SearxngURL != "" {
		searxngInst = search.NewSearXNG(c.SearxngURL, search.WithMetrics(reg))
	}

	// LLM client.
	llmOpts := []engllm.Option{
		engllm.WithAPIBase(c.LLMAPIBase),
		engllm.WithAPIKey(c.LLMAPIKey),
		engllm.WithModel(c.LLMModel),
		engllm.WithTemperature(c.LLMTemperature),
		engllm.WithMaxTokens(c.LLMMaxTokens),
		engllm.WithMetrics(reg),
	}
	if chain := engllm.ParseModelFallbackChain(c.LLMModelFallback); len(chain) > 0 {
		// Cross-provider model chain: primary model → each chain entry on retryable
		// failure. Internally uses WithEndpoints which disables key rotation
		// (WithAPIKeyFallbacks becomes a no-op). Chain OR key-rotation, not both.
		llmOpts = append(llmOpts, engllm.WithModelFallbackChain(chain))
		// Wire the health-filter observer so absent-model and degraded events
		// surface as Prometheus counters (gojob_llm_models_dropped_total /
		// gojob_llm_chain_degraded_total) rather than silent misfires.
		llmOpts = append(llmOpts, engllm.WithModelFilterObserver(func(ev engllm.ModelFilterEvent) {
			if ev.Degraded {
				reg.Incr(MetricLLMChainDegraded + "{reason=" + ev.Reason + "}")
				slog.Warn("llm: model chain filter degraded", slog.String("reason", ev.Reason))
				return
			}
			if len(ev.Dropped) > 0 {
				reg.Add(MetricLLMModelsDropped, int64(len(ev.Dropped)))
				slog.Warn("llm: models absent from /v1/models, dropped from chain",
					slog.Int("dropped", len(ev.Dropped)),
					slog.Any("models", ev.Dropped),
				)
			}
		}))
		slog.Info("llm: model fallback chain enabled", slog.Int("chain_len", len(chain)))
	} else if len(c.LLMAPIKeyFallbacks) > 0 {
		llmOpts = append(llmOpts, engllm.WithAPIKeyFallbacks(c.LLMAPIKeyFallbacks))
	}
	llmInst = engllm.New(llmOpts...)

	// Plain HTTP client for GitHub API and similar direct calls.
	httpClient = &http.Client{Timeout: 15 * time.Second}

	// Populate computed Config fields for sub-packages (jobs, sources).
	cfg.HTTPClient = httpClient
	cfg.BrowserClient = fetcherProxy.BrowserClient()

	slog.Info("engine: initialized",
		slog.Bool("proxy", c.ProxyPool != nil),
		slog.Bool("ddg", c.DirectDDG),
		slog.Bool("startpage", c.DirectStartpage),
		slog.Bool("brave", c.DirectBrave),
		slog.Bool("reddit", c.DirectReddit),
		slog.Bool("wikipedia", c.DirectWikipedia),
		slog.Bool("marginalia", c.DirectMarginalia),
	)
}

// Reg returns the package-level metrics registry for wiring middleware
// (e.g. mcpmw.Middleware) and any external Prometheus integration.
func Reg() *kitmetrics.Registry { return reg }
