// Package llm provides an OpenAI-compatible LLM client with retry
// and API key fallback rotation.
//
// Supports any OpenAI-compatible endpoint (CLIProxyAPI, Gemini, etc.)
// with automatic key rotation on quota exhaustion.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/anatolykoptev/go-engine/fetch"
	"github.com/anatolykoptev/go-engine/metrics"
)

const (
	defaultLLMTimeout  = 60 * time.Second
	defaultTemperature = 0.3
	defaultMaxTokens   = 2000
)

// Client communicates with an OpenAI-compatible LLM API.
type Client struct {
	apiBase         string
	apiKey          string
	apiKeyFallbacks []string
	model           string
	temperature     float64
	maxTokens       int
	httpClient      *http.Client
	metrics         *metrics.Registry
}

// Option configures a Client.
type Option func(*Client)

// WithAPIBase sets the API base URL (e.g. "http://127.0.0.1:8317/v1").
func WithAPIBase(url string) Option {
	return func(c *Client) { c.apiBase = url }
}

// WithAPIKey sets the primary API key.
func WithAPIKey(key string) Option {
	return func(c *Client) { c.apiKey = key }
}

// WithAPIKeyFallbacks sets fallback API keys for quota rotation.
func WithAPIKeyFallbacks(keys []string) Option {
	return func(c *Client) { c.apiKeyFallbacks = keys }
}

// WithModel sets the LLM model name.
func WithModel(model string) Option {
	return func(c *Client) { c.model = model }
}

// WithTemperature sets the default temperature.
func WithTemperature(t float64) Option {
	return func(c *Client) { c.temperature = t }
}

// WithMaxTokens sets the default max tokens.
func WithMaxTokens(n int) Option {
	return func(c *Client) { c.maxTokens = n }
}

// WithHTTPClient sets the HTTP client for LLM requests.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.httpClient = hc }
}

// WithMetrics sets the metrics registry.
func WithMetrics(m *metrics.Registry) Option {
	return func(c *Client) { c.metrics = m }
}

// New creates an LLM client.
func New(opts ...Option) *Client {
	c := &Client{
		httpClient:  &http.Client{Timeout: defaultLLMTimeout},
		temperature: defaultTemperature,
		maxTokens:   defaultMaxTokens,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// chatMessage is an OpenAI chat message.
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatRequest is an OpenAI chat completion request.
type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	MaxTokens   int           `json:"max_tokens"`
}

// chatResponse is an OpenAI chat completion response.
type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// Complete sends a prompt using the configured temperature and max_tokens.
func (c *Client) Complete(ctx context.Context, prompt string) (string, error) {
	return c.CompleteParams(ctx, prompt, c.temperature, c.maxTokens)
}

// CompleteParams sends a prompt with explicit temperature and maxTokens.
// On error, iterates through fallback keys until one succeeds.
func (c *Client) CompleteParams(ctx context.Context, prompt string, temperature float64, maxTokens int) (string, error) {
	raw, err := c.completeWithKey(ctx, prompt, temperature, maxTokens, c.apiKey)
	if err != nil {
		for _, key := range c.apiKeyFallbacks {
			if key == "" {
				continue
			}
			raw, err = c.completeWithKey(ctx, prompt, temperature, maxTokens, key)
			if err == nil {
				break
			}
		}
	}
	return raw, err
}

// completeWithKey performs a single LLM API call with the given key.
func (c *Client) completeWithKey(ctx context.Context, prompt string, temperature float64, maxTokens int, apiKey string) (string, error) {
	if c.metrics != nil {
		c.metrics.Incr("llm_calls")
	}

	body, err := json.Marshal(chatRequest{
		Model:       c.model,
		Messages:    []chatMessage{{Role: "user", Content: prompt}},
		Temperature: temperature,
		MaxTokens:   maxTokens,
	})
	if err != nil {
		c.incrErrors()
		return "", fmt.Errorf("marshal LLM request: %w", err)
	}

	apiURL := strings.TrimSuffix(c.apiBase, "/") + "/chat/completions"

	resp, err := fetch.RetryHTTP(ctx, fetch.DefaultRetryConfig, func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+apiKey)
		return c.httpClient.Do(req) //nolint:bodyclose,gosec // closed below; URL is config-provided
	})
	if err != nil {
		c.incrErrors()
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		c.incrErrors()
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("LLM API %d: %s", resp.StatusCode, string(respBody))
	}

	var chatResp chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return "", err
	}
	if len(chatResp.Choices) == 0 {
		return "", errors.New("no choices in LLM response")
	}

	raw := strings.TrimSpace(chatResp.Choices[0].Message.Content)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	return strings.TrimSpace(raw), nil
}

func (c *Client) incrErrors() {
	if c.metrics != nil {
		c.metrics.Incr("llm_errors")
	}
}

// currentDate returns today's date in ISO 8601 format (UTC).
func currentDate() string {
	return time.Now().UTC().Format("2006-01-02")
}
