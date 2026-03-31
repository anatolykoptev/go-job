package engine

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/anatolykoptev/go-engine/extract"
	"github.com/anatolykoptev/go-engine/fetch"
	engllm "github.com/anatolykoptev/go-engine/llm"
	"github.com/anatolykoptev/go-engine/metrics"
	"github.com/anatolykoptev/go-engine/search"
	"github.com/anatolykoptev/go-stealth/proxypool"
	twitter "github.com/anatolykoptev/go-twitter"
	"github.com/anatolykoptev/go_job/internal/social"
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
	IndeedAPIKey              string              // overrideable via INDEED_API_KEY env
	TwitterClient             *twitter.Client     // nil = Twitter search disabled
	SocialClient              *social.Client      // nil = go-social disabled, use local twitter
	DatabaseURL               string              // DATABASE_URL for PostgreSQL (resume graph)
	MemDBURL                  string              // MEMDB_URL for vector search
	MemDBServiceSecret        string              // INTERNAL_SERVICE_SECRET for MemDB auth
	EmbedURL                  string              // EMBED_URL for direct embedding server

	// Bounty search tuning.
	BountyHighConfidence float32 // cosine threshold for high-confidence tier (default 0.82)
	BountyHighConfGap    float32 // max gap from best in high-confidence tier (default 0.04)
	BountyHighConfMax    int     // max results in high-confidence tier (default 10)
	BountyMedConfMax     int     // max results in medium-confidence tier (default 3)
	BountySkillBoost     float32 // boost when query matches bounty skills (default 0.05)
	BountyMinRelevance   float32 // minimum best-score to return results (default 0.75)

	// Bounty monitor.
	VaelorNotifyURL       string        // VAELOR_NOTIFY_URL for sending Telegram notifications
	BountyNotifyChatID    string        // BOUNTY_NOTIFY_CHAT_ID (default "428660")
	BountyMonitorInterval time.Duration // BOUNTY_MONITOR_INTERVAL (default 15m)

	// Computed fields — populated by Init(), not set by caller.
	HTTPClient    *http.Client   // plain HTTP client for API calls
	BrowserClient *BrowserClient // proxy browser client (nil if no proxy)
}

// Package-level go-engine instances, set by Init().
var (
	cfg           Config
	fetcherProxy  *fetch.Fetcher     // with proxy, for web pages
	fetcherDirect *fetch.Fetcher     // no proxy, for raw content + internal APIs
	extractorInst *extract.Extractor // HTML content extraction
	searxngInst   *search.SearXNG    // SearXNG client
	llmInst       *engllm.Client     // LLM client
	reg           *metrics.Registry  // metrics counters
	httpClient    *http.Client       // plain HTTP client for GitHub API etc.
)

// Cfg exposes the engine configuration for sub-packages (jobs, sources).
var Cfg = &cfg

// Init initializes the engine with the given configuration.
func Init(c Config) {
	cfg = c
	Cfg = &cfg

	// Metrics registry.
	reg = metrics.New()

	// Fetcher with proxy (for web pages, direct scrapers).
	fetcherOpts := []fetch.Option{fetch.WithTimeout(c.FetchTimeout)}
	if c.ProxyPool != nil {
		fetcherOpts = append(fetcherOpts, fetch.WithProxyPool(c.ProxyPool))
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
	if len(c.LLMAPIKeyFallbacks) > 0 {
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
	)
}
