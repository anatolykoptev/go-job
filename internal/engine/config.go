package engine

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/anatolykoptev/go-engine/extract"
	"github.com/anatolykoptev/go-engine/fetch"
	engllm "github.com/anatolykoptev/go-engine/llm"
	"github.com/anatolykoptev/go-kit/env"
	kitmetrics "github.com/anatolykoptev/go-kit/metrics"
	linkedin "github.com/anatolykoptev/go-linkedin"
	"github.com/anatolykoptev/go-stealth/proxypool"
	twitter "github.com/anatolykoptev/go-twitter"
	"github.com/anatolykoptev/go-twitter/social"
	"github.com/prometheus/client_golang/prometheus"
)

// Config holds all engine configuration, injected from main.
type Config struct {
	LLMAPIKey              string
	LLMAPIKeyFallbacks     []string
	LLMAPIBase             string
	LLMModel               string
	LLMTemperature         float64
	LLMMaxTokens           int
	MaxFetchURLs           int
	MaxContentChars        int
	FetchTimeout           time.Duration
	GithubToken            string
	Context7APIKey         string
	HuggingFaceToken       string
	YouTubeAPIKey          string
	YouTubeAPIKeyFallback  string
	CacheMaxEntries        int
	CacheCleanupInterval   time.Duration
	ProxyPool              proxypool.ProxyPool // replaces BrowserClient + HTTPClient
	DirectDDG              bool                // enable DuckDuckGo direct scraper
	DirectStartpage        bool                // enable Startpage direct scraper
	DirectBrave            bool                // enable Brave direct scraper
	DirectReddit           bool                // enable Reddit direct scraper
	DirectWikipedia        bool                // enable Wikipedia direct scraper (DIRECT_WIKIPEDIA)
	DirectMarginalia       bool                // enable Marginalia direct scraper (DIRECT_MARGINALIA)
	SearchEarlyReturnAt    int                 // SEARCH_EARLY_RETURN_AT: soft result cap; 0 = go-engine default (10)
	SearchPerSourceTimeout time.Duration       // SEARCH_PER_SOURCE_TIMEOUT: per-source cap; 0 = go-engine default (6s)
	// FetchDirectFirst enables go-engine v1.12+ direct-first tiered fallback.
	// When true, the fetcher tries Chrome-TLS direct first and escalates to proxy
	// only on anti-bot signals (HTTP 401/403/429/503, Cloudflare/PerimeterX/DataDome
	// markers, soft-block heuristic, TLS errors). Controlled by FETCH_DIRECT_FIRST env var.
	FetchDirectFirst bool
	IndeedAPIKey     string           // overrideable via INDEED_API_KEY env
	TwitterClient    *twitter.Client  // nil = Twitter search disabled
	SocialClient     *social.Client   // nil = go-social disabled, use local twitter
	LinkedInClient   *linkedin.Client // nil = LinkedIn tools disabled
	DatabaseURL      string           // DATABASE_URL for PostgreSQL (resume graph)
	EmbedURL         string           // EMBED_URL for direct embedding server

	// OxBrowserURL is the base URL of the self-hosted ox-browser solver
	// (e.g. "http://ox-browser:8901"). When non-empty, the proxy fetcher gains
	// an ox-browser /fetch-smart fallback tier: on any primary-fetch error
	// (notably DDG's 202 anti-bot wall from a datacenter IP), the fetch
	// escalates to a real headless browser. Empty = disabled (graceful).
	// Authoritative source is OX_BROWSER_URL env — do NOT hard-code the
	// location, so a future move to the host-d mesh is a one-line env change.
	OxBrowserURL string // OX_BROWSER_URL

	// CraigslistDefaultLocation is the fallback location used when a craigslist
	// job_search is called with no explicit location AND the operator's resume
	// profile has no location (or its read timed out / errored). Empty by
	// default — when both the profile and this config value are empty, the
	// connector keeps its current errCraigslistUnmapped behaviour (fails rather
	// than silently searching the wrong city). Env: CRAIGSLIST_DEFAULT_LOCATION.
	//
	// NOTE: this config has no setter outside tests in the current single-
	// operator deployment — the operator's region comes from the resume
	// profile. It is retained as a deployment default for installations with no
	// profile and is validated against resolveRegion at startup (main.go) so a
	// value like "Salt Lake City" that does not map to a Craigslist area fails
	// fast instead of surfacing as wrong listings (#347).
	CraigslistDefaultLocation string

	// Bounty notify: env vars are read directly by go-kit's NewProductSinkFromEnv.
	// TELEGRAM_BOT_TOKEN and HUNT_NOTIFY_CHAT_ID are required at deploy.
	// VaelorNotifyURL and BountyNotifyChatID removed — no longer used.

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

	// LLMProxyURLs is a CSV of LLM proxy base URLs for multi-proxy rotation
	// (go-engine v1.51.5+ / go-kit v0.97.1+). When len > 1, the LLM client
	// builds a cross-product of proxies × models: local proxy tried first
	// for every model, remote as fallback. Proxy-level redundancy on top
	// of model-level fallback.
	//
	// Takes precedence over LLMAPIBase when len > 1. When len <= 1 (or
	// empty), behavior is identical to single-proxy mode (LLMAPIBase +
	// LLMAPIKey).
	//
	// MUTEX with LLMModelFallback: WithProxyURLs uses WithEndpoints
	// internally, which disables key rotation via WithAPIKeyFallbacks.
	// Use proxy rotation OR key rotation, not both.
	// Env: LLM_PROXY_URLS (mounted via config/llm.env).
	LLMProxyURLs []string

	// LLMProxyKeys is a CSV of API keys matching LLMProxyURLs (same length,
	// same order). If a proxy uses the same key as the primary, pass an
	// empty string at that position (it defaults to LLMAPIKey). If shorter
	// than LLMProxyURLs, missing keys default to LLMAPIKey.
	// Env: LLM_PROXY_KEYS (mounted via config/llm.env).
	LLMProxyKeys []string

	// Computed fields — populated by Init(), not set by caller.
	HTTPClient    *http.Client   // plain HTTP client for API calls
	BrowserClient *BrowserClient // stealth Chrome-TLS client; proxy-backed when ProxyPool is set, direct (no-proxy) in direct-first mode — never nil after Init unless both tiers are unavailable
}

// Package-level go-engine instances, set by Init().
var (
	cfg           Config
	fetcherProxy  *fetch.Fetcher       // with proxy, for web pages
	fetcherDirect *fetch.Fetcher       // no proxy, for raw content + internal APIs
	extractorInst *extract.Extractor   // HTML content extraction
	llmInst       *engllm.Client       // LLM client
	reg           *kitmetrics.Registry // metrics counters (Prometheus-bridged)
	httpClient    *http.Client         // plain HTTP client for GitHub API etc.

	// llmModelWeights is the captured value of LLM_MODEL_WEIGHTS at Init time.
	// go-kit reads it ONCE at client construction (go-kit/llm/client.go:353),
	// so it is a process-lifetime constant — reading os.Getenv per unparseable
	// failure would log a value that can diverge from what the live client
	// actually routes on. Captured here so the WARN in SummarizeJobResults
	// reports the same weights the client was built with.
	llmModelWeights string
)

// Cfg exposes the engine configuration for sub-packages (jobs, sources).
var Cfg = &cfg

// Init initializes the engine with the given configuration.
func Init(c Config) {
	cfg = c
	Cfg = &cfg
	llmModelWeights = os.Getenv("LLM_MODEL_WEIGHTS")

	// Metrics registry (Prometheus-bridged under "gojob" namespace).
	reg = kitmetrics.NewPrometheusRegistry("gojob")
	// Pre-register the alert-backing bounded matrices (platform×outcome,
	// discovery source) at zero so increase()-based alerts in
	// alerts-go-job.yml see a real 0→N transition on the FIRST occurrence
	// after a restart, not just the second. Must run before any traffic.
	warmAlertBoundedMetrics()
	// Pre-configure byte-scale buckets for the oversize histogram before any
	// Observe call. Must happen at startup (before traffic) — buckets lock at
	// first Observe. The seconds-shaped default (ExponentialBuckets(0.001,2,16))
	// is useless for bytes; OversizeBytesBuckets covers 1KB–4MB log-scale.
	reg.RegisterHistogram(MetricOversizeBytes, kitmetrics.WithBuckets(OversizeBytesBuckets))
	// Cycle-duration histogram: range 1s–10m, registered before first Observe.
	// Default ExponentialBuckets top out at ~32.8s — below a full 45s-per-platform
	// cycle ceiling, putting all slow runs in +Inf.  Explicit buckets fix this.
	reg.RegisterHistogram(MetricHuntCycleDuration, kitmetrics.WithBuckets(HuntCycleDurationBuckets))
	// Per-source duration histogram (ADR-J3, P3): range 0.1s–120s covers fast JSON
	// APIs through slow UN portal fan-outs. Registered before first Observe.
	reg.RegisterHistogram(MetricSourceDuration, kitmetrics.WithBuckets(SourceDurationBuckets))
	// Fit-score histogram (P6): range 0–100 in 20-point steps.
	// Answers "what is my fit distribution?" so the operator can tune HUNT_NOTIFY_MIN_FIT.
	reg.RegisterHistogram(MetricHuntFitScore, kitmetrics.WithBuckets(HuntFitScoreBuckets))
	// OBS-6: LLM request latency histogram — makes LLM slowness visible
	// before it hits timeout. Buckets: 0.1s–60s.
	reg.RegisterHistogram(MetricLLMRequestDuration, kitmetrics.WithBuckets(LLMRequestDurationBuckets))
	// OBS-6: admin UI request latency histogram.
	reg.RegisterHistogram(MetricAdminRequestDuration, kitmetrics.WithBuckets(AdminRequestDurationBuckets))

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
	// ox-browser anti-bot fallback tier. The go-engine fetcher's tryFallbacks
	// makes ox-browser the FIRST fallback on any primary-fetch error — this is
	// what defeats DDG's 202 wall on the slug-discovery path (datacenter IP →
	// real headless browser). Gated on OX_BROWSER_URL being set; empty = the
	// fetcher's oxBrowserURL stays "" and the tier is a no-op (graceful degrade).
	if c.OxBrowserURL != "" {
		fetcherOpts = append(fetcherOpts, fetch.WithOxBrowser(c.OxBrowserURL))
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

	// LLM client.
	if c.LLMAPIKey == "" {
		slog.Error("engine: LLM_API_KEY is empty — LLM-dependent features (hunt scoring, " +
			"query expansion, summarization) will fail with auth errors. Set LLM_API_KEY " +
			"or LLM_PROXY_KEYS to enable LLM features.")
	}
	llmOpts := []engllm.Option{
		engllm.WithAPIBase(c.LLMAPIBase),
		engllm.WithAPIKey(c.LLMAPIKey),
		engllm.WithModel(c.LLMModel),
		engllm.WithTemperature(c.LLMTemperature),
		engllm.WithMaxTokens(c.LLMMaxTokens),
		engllm.WithMetrics(reg),
	}
	// Multi-proxy rotation (go-engine v1.51.5+): when >1 proxy URL is
	// configured AND a model fallback chain is set, the LLM client builds
	// a cross-product of proxies × models — local proxy first per model,
	// remote as fallback. Proxy-level redundancy on top of model-level
	// fallback. When <=1 proxy URL, behavior is identical to single-proxy
	// mode (WithAPIBase + WithAPIKey).
	if len(c.LLMProxyURLs) > 1 {
		// PF-9 fix: validate proxy URL/key length mismatch before wiring.
		// If LLMProxyKeys is shorter than LLMProxyURLs, missing keys default
		// to LLMAPIKey — but if LLMAPIKey is also empty, all remote proxy
		// attempts will fail with auth errors. Surface this at startup.
		if len(c.LLMProxyKeys) > 0 && len(c.LLMProxyKeys) < len(c.LLMProxyURLs) {
			slog.Error("llm: LLM_PROXY_KEYS shorter than LLM_PROXY_URLS — missing keys default to LLM_API_KEY",
				slog.Int("proxy_urls", len(c.LLMProxyURLs)),
				slog.Int("proxy_keys", len(c.LLMProxyKeys)),
				slog.Bool("llm_api_key_empty", c.LLMAPIKey == ""),
			)
			if c.LLMAPIKey == "" {
				slog.Error("llm: LLM_API_KEY is empty AND proxy keys are missing — all remote proxy auth will fail")
			}
		}
		llmOpts = append(llmOpts, engllm.WithProxyURLs(c.LLMProxyURLs))
		llmOpts = append(llmOpts, engllm.WithProxyKeys(c.LLMProxyKeys))
		slog.Info("llm: multi-proxy rotation enabled",
			slog.Int("proxies", len(c.LLMProxyURLs)))
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

	// #181: Validate LLM API key at startup with a minimal ping. Non-fatal —
	// if the key is invalid/expired, log an ERROR but continue (fail-open path
	// will produce unscored results, which is better than refusing to start).
	// The metric hunt_score_llm_total{llm_result="llm_error"} will also fire
	// on every scoring attempt, and the GojobHuntScoringDegraded alert covers
	// sustained degradation.
	go func() {
		validateCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := llmInst.Complete(validateCtx, "ping"); err != nil {
			slog.Error("engine: LLM API key validation failed — scoring will degrade to unscored",
				slog.Any("error", err),
				slog.String("hint", "check LLM_API_KEY / LLM_API_BASE — key may be expired or revoked"))
		} else {
			slog.Info("engine: LLM API key validation OK")
		}
	}()

	// Plain HTTP client for GitHub API and similar direct calls.
	// Configured with connection pooling to prevent FD exhaustion under
	// high parallel load (PF-13 fix: MaxIdleConns/MaxConnsPerHost/IdleConnTimeout).
	httpClient = &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxConnsPerHost:     10,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
			TLSHandshakeTimeout: 10 * time.Second,
		},
	}

	// Populate computed Config fields for sub-packages (jobs, sources).
	cfg.HTTPClient = httpClient
	// BrowserClient: prefer the proxy-backed stealth client; fall back to the
	// no-proxy direct Chrome-TLS client when running in direct-first mode
	// (FETCH_DIRECT_FIRST=direct → ProxyPool nil → BrowserClient() nil).
	// A stealth HTTP client does not need a proxy to be useful — its value
	// is the TLS/JA3/header fingerprint. Without this fallback, connectors
	// that guard on `Cfg.BrowserClient != nil` (e.g. Craigslist RSS) silently
	// skip the stealth tier in direct mode and report empty results.
	cfg.BrowserClient = resolveBrowserClient(fetcherProxy.BrowserClient(), fetcherProxy.DirectClient())

	slog.Info("engine: initialized",
		slog.Bool("proxy", c.ProxyPool != nil),
		slog.Bool("oxbrowser", c.OxBrowserURL != ""),
		slog.Bool("ddg", c.DirectDDG),
		slog.Bool("startpage", c.DirectStartpage),
		slog.Bool("brave", c.DirectBrave),
		slog.Bool("reddit", c.DirectReddit),
		slog.Bool("wikipedia", c.DirectWikipedia),
		slog.Bool("marginalia", c.DirectMarginalia),
	)

	// Surface the 202-risk: DDG discovery from a datacenter IP with NO anti-bot
	// escalation tier (ox-browser unwired) is exactly the configuration that
	// collapsed discovery on 2026-06-22. Make it visible at startup rather than
	// letting the operator discover it via an empty hunt table.
	if c.DirectDDG && c.OxBrowserURL == "" {
		slog.Error("engine: DDG discovery enabled WITHOUT an ox-browser anti-bot tier — " +
			"a datacenter-IP 202 wall will silently zero discovery; set OX_BROWSER_URL")
	}

	// #167: Surface the fail-open setting at startup so operators know whether
	// LLM failures will silently degrade to unscored (true) or produce errors (false).
	if env.MustBool("HUNT_SCORE_FAIL_OPEN", false) {
		slog.Warn("engine: HUNT_SCORE_FAIL_OPEN=true — LLM failures will silently degrade to unscored")
	}
}

// Reg returns the package-level metrics registry for wiring middleware
// (e.g. mcpmw.Middleware) and any external Prometheus integration.
func Reg() *kitmetrics.Registry { return reg }

// resolveBrowserClient picks the proxy-backed stealth BrowserClient, falling
// back to the no-proxy direct Chrome-TLS client when the proxy-backed one is
// nil (direct-first mode: FETCH_DIRECT_FIRST=direct → ProxyPool nil →
// BrowserClient() nil). A stealth HTTP client does not need a proxy to be
// useful — its value is the TLS/JA3/header fingerprint. Without this fallback,
// connectors that guard on `Cfg.BrowserClient != nil` (e.g. Craigslist)
// silently skip the stealth tier in direct mode and report empty results.
//
// Returns nil only when BOTH clients are nil (neither tier was built — e.g.
// go-engine built no stealth client at all).
//
// Extracted from Init() so the fallback is unit-testable in isolation (H5):
// Init() is too heavy to exercise (goroutines, metrics, LLM ping), and every
// unit test overrides the craigslist transport vars wholesale, so the real
// fallback body never ran under test — reverting it would not turn anything red.
func resolveBrowserClient(proxy, direct *BrowserClient) *BrowserClient {
	if proxy != nil {
		return proxy
	}
	return direct
}

// InitTestRegistry replaces the package-level metrics registry with a fresh
// in-memory registry. For use in tests in other packages that call metric
// functions (e.g. IncrHuntDiscoverySource) and need to read counter deltas via
// GetMetrics(). Call once per test binary via TestMain.
func InitTestRegistry() {
	reg = kitmetrics.NewRegistry()
	scoringDegradedState.Store(false)
}
