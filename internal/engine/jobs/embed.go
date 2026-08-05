package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	kitembed "github.com/anatolykoptev/go-kit/embed"
)

// EmbedClient calls an OpenAI-compatible embedding server.
// Used by tests and as a lower-level fallback; production code uses the
// kitembed.Embedder singleton set by SetEmbedClient.
type EmbedClient struct {
	baseURL string
	http    *http.Client
}

// NewEmbedClient creates an embed client for the given base URL.
func NewEmbedClient(baseURL string) *EmbedClient {
	return &EmbedClient{
		baseURL: baseURL,
		http:    &http.Client{Timeout: 60 * time.Second},
	}
}

type embedRequest struct {
	Input []string `json:"input"`
	Model string   `json:"model,omitempty"`
}

type embedResponse struct {
	Object string      `json:"object"`
	Data   []embedData `json:"data"`
	Model  string      `json:"model"`
}

type embedData struct {
	Object    string    `json:"object"`
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}

// EmbedPassages sends passage texts (prefixed with kitembed.E5PassagePrefix for e5-large retrieval mode).
func (c *EmbedClient) EmbedPassages(ctx context.Context, texts []string) ([][]float32, error) {
	prefixed := make([]string, len(texts))
	for i, t := range texts {
		prefixed[i] = kitembed.E5PassagePrefix + t
	}
	return c.embedRaw(ctx, prefixed)
}

// EmbedQuery sends a single query text (prefixed with kitembed.E5QueryPrefix for e5-large retrieval mode).
func (c *EmbedClient) EmbedQuery(ctx context.Context, query string) ([]float32, error) {
	vecs, err := c.embedRaw(ctx, []string{kitembed.E5QueryPrefix + query})
	if err != nil {
		return nil, err
	}
	if len(vecs) == 0 || len(vecs[0]) == 0 {
		return nil, errors.New("empty query vector")
	}
	return vecs[0], nil
}

// EmbedTexts sends texts to the embedding server and returns vectors.
//
// Deprecated: use EmbedPassages or EmbedQuery for proper e5-large retrieval.
func (c *EmbedClient) EmbedTexts(ctx context.Context, texts []string) ([][]float32, error) {
	return c.embedRaw(ctx, texts)
}

// embedRaw sends texts to the embedding server and returns vectors.
func (c *EmbedClient) embedRaw(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	body, err := json.Marshal(embedRequest{Input: texts})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embed-server returned status %d", resp.StatusCode)
	}

	var result embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	vecs := make([][]float32, len(result.Data))
	for _, d := range result.Data {
		if d.Index < len(vecs) {
			vecs[d.Index] = d.Embedding
		}
	}
	return vecs, nil
}

// Package-level embed client (go-kit Embedder), set by SetEmbedClient.
// Production code constructs this via kitembed.NewClient which auto-resolves
// EMBED_TOKEN from the environment and handles retries/circuit-breaking.
var embedClient kitembed.Embedder

// SetEmbedClient sets the package-level embedder.
func SetEmbedClient(c kitembed.Embedder) { embedClient = c }

// GetEmbedClient returns the package-level embedder (nil if not configured).
func GetEmbedClient() kitembed.Embedder { return embedClient }
