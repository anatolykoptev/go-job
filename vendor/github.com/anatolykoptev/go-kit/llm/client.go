// Package llm provides an OpenAI-compatible LLM client with retry and fallback keys.
// Supports text and multimodal (vision) requests. Zero external dependencies
// beyond net/http. Designed to replace duplicated LLM clients across go-* services.
package llm

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// Default client constants.
const (
	defaultMaxTokens  = 8192
	defaultMaxRetries = 3
	defaultTimeout    = 90 * time.Second
	retryDelay        = 500 * time.Millisecond
	maxRetryDelay     = 5 * time.Second
)

// Client is an OpenAI-compatible LLM client with retry and fallback key support.
type Client struct {
	baseURL      string
	apiKey       string
	model        string
	maxTokens    int
	temperature  float64
	httpClient   *http.Client
	fallbackKeys []string
	maxRetries   int
	endpoints    []Endpoint
	middleware   []Middleware
}

// Option configures the Client.
type Option func(*Client)

// WithFallbackKeys sets fallback API keys tried when the primary gets 429/5xx.
func WithFallbackKeys(keys []string) Option {
	return func(c *Client) { c.fallbackKeys = keys }
}

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.httpClient = hc }
}

// WithMaxTokens sets the max tokens for completions.
func WithMaxTokens(n int) Option {
	return func(c *Client) { c.maxTokens = n }
}

// WithTemperature sets the sampling temperature.
func WithTemperature(t float64) Option {
	return func(c *Client) { c.temperature = t }
}

// WithMaxRetries sets how many times to retry on retryable errors.
func WithMaxRetries(n int) Option {
	return func(c *Client) { c.maxRetries = n }
}

// Endpoint defines a complete API endpoint for fallback chains.
// Each endpoint can have its own URL, API key, and model.
type Endpoint struct {
	URL   string
	Key   string
	Model string
}

// WithEndpoints sets fallback endpoint chains. Each endpoint is tried
// in order on retryable errors. Overrides the base URL/key/model when set.
func WithEndpoints(endpoints []Endpoint) Option {
	return func(c *Client) { c.endpoints = endpoints }
}

// Middleware wraps chat completion calls. Use for logging, metrics, caching.
// The next function sends the request to the API (or the next middleware).
// First added middleware is the outermost wrapper.
type Middleware func(ctx context.Context, req *ChatRequest, next func(context.Context, *ChatRequest) (*ChatResponse, error)) (*ChatResponse, error)

// WithMiddleware adds a middleware to the execution pipeline.
func WithMiddleware(m Middleware) Option {
	return func(c *Client) { c.middleware = append(c.middleware, m) }
}

// NewClient creates a new LLM client.
func NewClient(baseURL, apiKey, model string, opts ...Option) *Client {
	c := &Client{
		baseURL:     strings.TrimRight(baseURL, "/"),
		apiKey:      apiKey,
		model:       model,
		maxTokens:   defaultMaxTokens,
		temperature: 0.1,
		maxRetries:  defaultMaxRetries,
		httpClient:  &http.Client{Timeout: defaultTimeout},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Complete sends a text completion request with optional system prompt.
// If system is empty, only the user message is sent.
// Optional ChatOptions (e.g. WithChatTemperature, WithChatMaxTokens) override client defaults for this call.
func (c *Client) Complete(ctx context.Context, system, user string, opts ...ChatOption) (string, error) {
	var msgs []Message
	if system != "" {
		msgs = append(msgs, Message{Role: "system", Content: system})
	}
	msgs = append(msgs, Message{Role: "user", Content: user})
	return c.CompleteRaw(ctx, msgs, opts...)
}

// CompleteMultimodal sends a vision request with text + images.
// Optional ChatOptions (e.g. WithChatTemperature, WithChatMaxTokens) override client defaults for this call.
func (c *Client) CompleteMultimodal(ctx context.Context, prompt string, images []ImagePart, opts ...ChatOption) (string, error) {
	parts := []ContentPart{{Type: "text", Text: prompt}}
	for _, img := range images {
		parts = append(parts, ContentPart{
			Type:     "image_url",
			ImageURL: &ImageURL{URL: img.URL},
		})
	}
	msgs := []Message{{Role: "user", Content: parts}}
	return c.CompleteRaw(ctx, msgs, opts...)
}

// CompleteRaw sends a chat completion with explicit messages.
// Retries on 429/5xx, cycles through fallback keys.
// Optional ChatOptions (e.g. WithChatTemperature, WithChatMaxTokens) override client defaults for this call.
func (c *Client) CompleteRaw(ctx context.Context, messages []Message, opts ...ChatOption) (string, error) {
	req := c.newRequest(messages)
	if len(opts) > 0 {
		var cfg chatConfig
		for _, opt := range opts {
			opt(&cfg)
		}
		cfg.apply(req)
	}
	resp, err := c.execute(ctx, req)
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}

func (c *Client) newRequest(messages []Message) *ChatRequest {
	return &ChatRequest{
		Model:       c.model,
		Messages:    messages,
		Temperature: c.temperature,
		MaxTokens:   c.maxTokens,
	}
}

func (c *Client) execute(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	if len(c.middleware) == 0 {
		return c.executeInner(ctx, req)
	}
	return c.buildChain(0)(ctx, req)
}

func (c *Client) buildChain(i int) func(context.Context, *ChatRequest) (*ChatResponse, error) {
	if i >= len(c.middleware) {
		return c.executeInner
	}
	return func(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
		return c.middleware[i](ctx, req, c.buildChain(i+1))
	}
}

// ExtractJSON extracts a JSON value from LLM output that may be wrapped
// in markdown code fences or surrounded by text.
// Handles both JSON objects ({...}) and arrays ([...]).
func ExtractJSON(s string) string {
	// Try markdown ```json ... ``` first.
	start := strings.Index(s, "```json")
	if start >= 0 {
		s = s[start+7:]
		end := strings.Index(s, "```")
		if end >= 0 {
			return strings.TrimSpace(s[:end])
		}
	}
	// Try plain ``` ... ``` fences.
	start = strings.Index(s, "```")
	if start >= 0 {
		inner := s[start+3:]
		end := strings.Index(inner, "```")
		if end >= 0 {
			return strings.TrimSpace(inner[:end])
		}
	}
	// Fall back to finding first/last matching delimiters.
	// Try object first, then array, pick whichever starts earlier.
	firstObj := strings.IndexByte(s, '{')
	lastObj := strings.LastIndexByte(s, '}')
	firstArr := strings.IndexByte(s, '[')
	lastArr := strings.LastIndexByte(s, ']')

	objOK := firstObj >= 0 && lastObj > firstObj
	arrOK := firstArr >= 0 && lastArr > firstArr

	switch {
	case objOK && arrOK:
		if firstObj <= firstArr {
			return s[firstObj : lastObj+1]
		}
		return s[firstArr : lastArr+1]
	case objOK:
		return s[firstObj : lastObj+1]
	case arrOK:
		return s[firstArr : lastArr+1]
	}
	return s
}
