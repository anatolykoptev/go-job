package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"time"
)

// EmbedClient calls an OpenAI-compatible embedding server.
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

// EmbedPassages sends passage texts (prefixed with "passage: " for e5-large retrieval mode).
func (c *EmbedClient) EmbedPassages(ctx context.Context, texts []string) ([][]float32, error) {
	prefixed := make([]string, len(texts))
	for i, t := range texts {
		prefixed[i] = "passage: " + t
	}
	return c.embedRaw(ctx, prefixed)
}

// EmbedQuery sends a single query text (prefixed with "query: " for e5-large retrieval mode).
func (c *EmbedClient) EmbedQuery(ctx context.Context, query string) ([]float32, error) {
	vecs, err := c.embedRaw(ctx, []string{"query: " + query})
	if err != nil {
		return nil, err
	}
	if len(vecs) == 0 || len(vecs[0]) == 0 {
		return nil, fmt.Errorf("empty query vector")
	}
	return vecs[0], nil
}

// EmbedTexts sends texts to the embedding server and returns vectors.
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
	defer resp.Body.Close()

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

// Healthy checks if embed-server is reachable.
func (c *EmbedClient) Healthy(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// CosineSimilarity computes cosine similarity between two vectors.
func CosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return float32(dot / denom)
}

// Package-level embed client, set by SetEmbedClient.
var embedClient *EmbedClient

// SetEmbedClient sets the package-level embed client.
func SetEmbedClient(c *EmbedClient) { embedClient = c }

// GetEmbedClient returns the package-level embed client (nil if not configured).
func GetEmbedClient() *EmbedClient { return embedClient }
