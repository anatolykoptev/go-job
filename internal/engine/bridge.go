package engine

// bridge.go provides wrapper functions that match the old engine API signatures,
// delegating to go-engine package instances. This allows all existing tool handlers,
// pipeline code, sources, and jobs packages to work unchanged.

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	stealth "github.com/anatolykoptev/go-stealth"

	"github.com/anatolykoptev/go-engine/fetch"
	"github.com/anatolykoptev/go-engine/llm"
	"github.com/anatolykoptev/go-engine/pipeline"
	"github.com/anatolykoptev/go-engine/text"
)

// ---- Type aliases ----

// BrowserClient is the stealth browser client type used for proxy HTTP calls.
//
// SECURITY NOTE (#186): go-stealth uses InsecureSkipVerify=true for TLS to
// avoid anti-bot fingerprinting. This is intentional for web scraping — MITM
// risk is accepted as part of the threat model. Sensitive data (API keys,
// credentials, tokens) MUST NEVER be transmitted through stealth clients.
// Only public web page content is fetched through this path.
type BrowserClient = stealth.BrowserClient

// RetryConfig controls retry behavior.
type RetryConfig = fetch.RetryConfig

// DefaultRetryConfig is suitable for most HTTP calls.
var DefaultRetryConfig = fetch.DefaultRetryConfig

// ---- Text utilities ----

// CleanHTML strips HTML tags and trims whitespace.
func CleanHTML(s string) string { return text.CleanHTML(s) }

// Truncate returns the first n bytes of s.
func Truncate(s string, n int) string { return text.Truncate(s, n) }

// TruncateRunes caps s at limit runes, appending suffix if truncated.
func TruncateRunes(s string, limit int, suffix string) string {
	return text.TruncateRunes(s, limit, suffix)
}

// TruncateAtWord truncates a string to maxLen runes at a word boundary.
func TruncateAtWord(s string, maxLen int) string {
	return text.TruncateAtWord(s, maxLen)
}

// NormLang normalises a language field: empty string -> LangAll.
func NormLang(lang string) string {
	if lang == "" {
		return LangAll
	}
	return lang
}

// LangAll is the sentinel value meaning "all languages".
const LangAll = "all"

// RandomUserAgent returns a random Chrome-like User-Agent.
func RandomUserAgent() string { return fetch.RandomUserAgent() }

// ChromeHeaders returns common Chrome browser headers.
func ChromeHeaders() map[string]string { return fetch.ChromeHeaders() }

// User-Agent strings used across HTTP clients.
const (
	UserAgentBot    = "GoJob/1.0"
	UserAgentChrome = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

// ---- Retry ----

// RetryDo retries fn up to MaxRetries times with exponential backoff.
func RetryDo[T any](ctx context.Context, rc RetryConfig, fn func() (T, error)) (T, error) {
	return fetch.RetryDo(ctx, rc, fn)
}

// RetryHTTP executes an HTTP request function with retry logic.
func RetryHTTP(ctx context.Context, rc RetryConfig, fn func() (*http.Response, error)) (*http.Response, error) {
	return fetch.RetryHTTP(ctx, rc, fn)
}

// ---- Query detection ----

// DetectQueryType classifies query by simple pattern matching.
func DetectQueryType(query string) QueryType { return text.DetectQueryType(query) }

// DetectQueryDomain classifies query by domain-specific patterns.
func DetectQueryDomain(query string) QueryDomain { return text.DetectQueryDomain(query) }

// ExtractLibraryName detects a known library/framework name from a query string.
func ExtractLibraryName(query string) string { return text.ExtractLibraryName(query) }

// ---- Fetch + Extract ----

// FetchURLContent extracts main text content from a URL.
// Returns (title, content, error). Falls back through extraction tiers.
func FetchURLContent(ctx context.Context, rawURL string) (title, content string, err error) {
	reg.Incr(MetricFetchRequests)
	defer func() {
		if err != nil {
			reg.Incr(MetricFetchErrors)
		}
	}()

	ctx, cancel := context.WithTimeout(ctx, cfg.FetchTimeout)
	defer cancel()

	body, err := fetcherProxy.FetchBody(ctx, rawURL)
	if err != nil {
		return "", "", err
	}

	parsedURL, _ := url.Parse(rawURL)
	result, err := extractorInst.Extract(ctx, body, parsedURL)
	if err != nil {
		return "", "", err
	}

	txt := result.Content
	txt = strings.TrimSpace(txt)
	if len(txt) > cfg.MaxContentChars {
		txt = txt[:cfg.MaxContentChars] + "..."
	}
	return result.Title, txt, nil
}

// FetchRawContent fetches a URL as plain text (no readability extraction).
// Uses direct fetcher (no proxy) for API-like endpoints.
func FetchRawContent(ctx context.Context, rawURL string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.FetchTimeout)
	defer cancel()

	body, err := fetcherDirect.FetchBody(ctx, rawURL)
	if err != nil {
		return "", err
	}

	txt := strings.TrimSpace(string(body))
	if len(txt) > cfg.MaxContentChars {
		txt = txt[:cfg.MaxContentChars] + "..."
	}
	return txt, nil
}

// ---- Output ----

// DefaultOutputOpts is the compact default for pipeline-based tools.
var DefaultOutputOpts = pipeline.DefaultOutputOpts

// FormatOutput trims SmartSearchOutput to fit within the given budget.
func FormatOutput(out SmartSearchOutput, opts OutputOpts) SmartSearchOutput {
	return pipeline.FormatOutput(out, opts)
}

// BuildSearchOutput constructs SmartSearchOutput with sources and facts.
func BuildSearchOutput(query string, llmOut *LLMStructuredOutput, results []SearxngResult) SmartSearchOutput {
	return pipeline.BuildSearchOutput(query, llmOut, results)
}

// FetchResult holds the outcome for a single parallel URL fetch.
type FetchResult = pipeline.FetchResult

// ParallelFetch fetches URL content in parallel, returning results per URL.
func ParallelFetch(ctx context.Context, urls []string, fetchFn func(ctx context.Context, url string) (string, error)) map[string]string {
	results := pipeline.ParallelFetch(ctx, urls, fetchFn)
	m := make(map[string]string, len(results))
	for _, r := range results {
		if r.Err == nil && r.Content != "" {
			m[r.URL] = r.Content
		}
	}
	return m
}

// ExtractJSONAnswer extracts the "answer" field from malformed JSON.
func ExtractJSONAnswer(raw string) string {
	return llm.ExtractJSONAnswer(raw)
}

// ExtractHTMLText extracts main readable text from an already-fetched HTML body.
// pageURL is the origin URL used as a readability hint; pass "" for unknown origin.
// Returns the extracted content string. Falls back through extraction tiers
// (trafilatura -> goquery -> regex). Follows the same path as [FetchURLContent]
// without the network fetch step.
func ExtractHTMLText(ctx context.Context, htmlBody string, pageURL string) (string, error) {
	parsedURL, _ := url.Parse(pageURL)
	result, err := extractorInst.Extract(ctx, []byte(htmlBody), parsedURL)
	if err != nil {
		return "", err
	}
	txt := strings.TrimSpace(result.Content)
	if cfg.MaxContentChars > 0 && len(txt) > cfg.MaxContentChars {
		txt = txt[:cfg.MaxContentChars] + "..."
	}
	return txt, nil
}

// FetchProxyBody fetches the raw response body through the hardened proxy fetcher
// (DirectFirst Chrome-TLS → Webshare proxy pool → oxbrowser/byparr CF-solver).
// Use for bot-protected JSON/raw APIs where FetchRawContent's direct-only path
// is insufficient. Returns raw bytes on success, error on permanent failure.
// This is the canonical seam for any future CF-blocked source.
func FetchProxyBody(ctx context.Context, rawURL string, extra map[string]string) ([]byte, error) {
	if fetcherProxy == nil {
		return nil, errors.New("proxy fetcher not initialized")
	}
	return fetcherProxy.FetchBodyWithHeaders(ctx, rawURL, extra)
}
