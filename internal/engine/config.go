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
)

// Config holds all engine configuration, injected from main.
type Config struct {
	SearxngURL           string
	LLMAPIKey            string
	LLMAPIKeyFallbacks   []string
	LLMAPIBase           string
	LLMModel             string
	LLMTemperature       float64
	LLMMaxTokens         int
	MaxFetchURLs         int
	MaxContentChars      int
	FetchTimeout         time.Duration
	GithubToken          string
	GithubSearchRepos    []string
	Context7APIKey       string
	HuggingFaceToken     string
	YouTubeAPIKey             string
	YouTubeAPIKeyFallback     string
	YouTubeTranscriptsEnabled bool
	CacheMaxEntries      int
	CacheCleanupInterval time.Duration
	ProxyPool            proxypool.ProxyPool // replaces BrowserClient + HTTPClient
	DirectDDG            bool                // enable DuckDuckGo direct scraper
	DirectStartpage      bool                // enable Startpage direct scraper
	IndeedAPIKey         string              // overrideable via INDEED_API_KEY env
	TwitterClient        *twitter.Client     // nil = Twitter search disabled
	DatabaseURL          string              // DATABASE_URL for PostgreSQL (resume graph)
	MemDBURL             string              // MEMDB_URL for vector search
	MemDBServiceSecret   string              // INTERNAL_SERVICE_SECRET for MemDB auth

	// Computed fields — populated by Init(), not set by caller.
	HTTPClient    *http.Client    // plain HTTP client for API calls
	BrowserClient *BrowserClient  // proxy browser client (nil if no proxy)
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

	// SearXNG client (local, no proxy needed).
	searxngInst = search.NewSearXNG(c.SearxngURL, search.WithMetrics(reg))

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
	)
}
