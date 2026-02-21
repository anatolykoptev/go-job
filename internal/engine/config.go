package engine

import (
	"net/http"
	"time"

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
	HTTPClient           *http.Client
	BrowserClient        *BrowserClient // nil = direct scrapers disabled
	DirectDDG            bool           // enable DuckDuckGo direct scraper
	DirectStartpage      bool           // enable Startpage direct scraper
	IndeedAPIKey         string         // hardcoded iOS app key; overrideable via INDEED_API_KEY env
	TwitterClient        *twitter.Client // nil = Twitter search disabled
}

var cfg Config

// Cfg exposes the engine configuration for sub-packages (jobs, sources).
// Always points to the current cfg value.
var Cfg = &cfg

// Init initializes the engine with the given configuration.
func Init(c Config) {
	cfg = c
	Cfg = &cfg
}
