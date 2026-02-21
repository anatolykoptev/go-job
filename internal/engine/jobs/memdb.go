package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// MemDBClient talks to the memdb-go HTTP API.
type MemDBClient struct {
	baseURL       string
	serviceSecret string
	http          *http.Client
}

// NewMemDBClient creates a MemDB client.
func NewMemDBClient(baseURL, serviceSecret string) *MemDBClient {
	return &MemDBClient{
		baseURL:       baseURL,
		serviceSecret: serviceSecret,
		http:          &http.Client{Timeout: 60 * time.Second},
	}
}

// Add sends a memory to MemDB for enrichment.
func (c *MemDBClient) Add(ctx context.Context, content string, info map[string]any) error {
	body := map[string]any{
		"user_id":           "gojob",
		"writable_cube_ids": []string{"gojob"},
		"memory_content":    content,
		"mode":              "fine",
		"async_mode":        "sync",
	}
	if len(info) > 0 {
		body["info"] = info
	}

	resp, err := c.post(ctx, "/product/add", body)
	if err != nil {
		return fmt.Errorf("memdb add: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("memdb add: status %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// MemDBSearchResult is a single result from MemDB search.
type MemDBSearchResult struct {
	Content  string         `json:"memory_content"`
	Score    float64        `json:"relativity"`
	Info     map[string]any `json:"info,omitempty"`
	MemoryID string         `json:"memory_id,omitempty"`
}

// Search queries MemDB for relevant memories.
func (c *MemDBClient) Search(ctx context.Context, query string, topK int, relativity float64) ([]MemDBSearchResult, error) {
	body := map[string]any{
		"user_id":           "gojob",
		"readable_cube_ids": []string{"gojob"},
		"query":             query,
		"top_k":             topK,
		"relativity":        relativity,
	}

	resp, err := c.post(ctx, "/product/search", body)
	if err != nil {
		return nil, fmt.Errorf("memdb search: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("memdb search: status %d: %s", resp.StatusCode, string(b))
	}

	var raw struct {
		Data struct {
			TextMem []struct {
				Memories []struct {
					Memory   string `json:"memory"`
					Metadata struct {
						Relativity float64        `json:"relativity"`
						Info       map[string]any `json:"info"`
						ID         string         `json:"id"`
					} `json:"metadata"`
				} `json:"memories"`
			} `json:"text_mem"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("memdb search decode: %w", err)
	}

	var results []MemDBSearchResult
	for _, cube := range raw.Data.TextMem {
		for _, m := range cube.Memories {
			results = append(results, MemDBSearchResult{
				Content:  m.Memory,
				Score:    m.Metadata.Relativity,
				Info:     m.Metadata.Info,
				MemoryID: m.Metadata.ID,
			})
		}
	}
	return results, nil
}

// DeleteByUser deletes all memories for the gojob user/cube.
func (c *MemDBClient) DeleteByUser(ctx context.Context, memoryIDs []string) error {
	if len(memoryIDs) == 0 {
		return nil
	}
	body := map[string]any{
		"user_id":    "gojob",
		"memory_ids": memoryIDs,
	}

	resp, err := c.post(ctx, "/product/delete_memory", body)
	if err != nil {
		return fmt.Errorf("memdb delete: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("memdb delete: status %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// ClearAllBySearch iteratively searches and deletes all memories for gojob.
func (c *MemDBClient) ClearAllBySearch(ctx context.Context) error {
	for {
		results, err := c.Search(ctx, "resume experience project skill achievement", 100, 0.0)
		if err != nil {
			return fmt.Errorf("memdb clear search: %w", err)
		}
		if len(results) == 0 {
			return nil
		}
		var ids []string
		for _, r := range results {
			if r.MemoryID != "" {
				ids = append(ids, r.MemoryID)
			}
		}
		if len(ids) == 0 {
			return nil
		}
		if err := c.DeleteByUser(ctx, ids); err != nil {
			return fmt.Errorf("memdb clear delete: %w", err)
		}
	}
}

func (c *MemDBClient) post(ctx context.Context, path string, body any) (*http.Response, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.serviceSecret != "" {
		req.Header.Set("X-Internal-Service", c.serviceSecret)
	}

	return c.http.Do(req)
}
