package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/anatolykoptev/go_job/internal/engine"
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

// AddResult holds the response from a MemDB add operation.
type AddResult struct {
	MemoryID string
}

// Add sends a memory to MemDB for enrichment and returns the new memory ID.
func (c *MemDBClient) Add(ctx context.Context, content string, info map[string]any) (*AddResult, error) {
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
		return nil, fmt.Errorf("memdb add: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("memdb add: status %d: %s", resp.StatusCode, string(b))
	}

	var raw struct {
		Data []struct {
			MemoryID string `json:"memory_id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		// Add succeeded but we couldn't parse the response — not fatal.
		return &AddResult{}, nil
	}
	result := &AddResult{}
	if len(raw.Data) > 0 {
		result.MemoryID = raw.Data[0].MemoryID
	}
	return result, nil
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

// DeleteByUser deletes the specified memories for the gojob user/cube.
// Retries on HTTP 500 (e.g. Postgres 40P01 deadlock) using engine.DefaultRetryConfig.
func (c *MemDBClient) DeleteByUser(ctx context.Context, memoryIDs []string) error {
	_, err := c.deleteByUserWithCount(ctx, memoryIDs)
	return err
}

// deleteByUserWithCount deletes the specified memories and returns the deleted_count
// reported by the server. Used by ClearAllBySearch to detect stuck-loop conditions.
func (c *MemDBClient) deleteByUserWithCount(ctx context.Context, memoryIDs []string) (int64, error) {
	if len(memoryIDs) == 0 {
		return 0, nil
	}
	body := map[string]any{
		"user_id":    "gojob",
		"memory_ids": memoryIDs,
	}

	resp, err := engine.RetryHTTP(ctx, engine.DefaultRetryConfig, func() (*http.Response, error) {
		return c.post(ctx, "/product/delete_memory", body)
	})
	if err != nil {
		return 0, fmt.Errorf("memdb delete: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("memdb delete: status %d: %s", resp.StatusCode, string(b))
	}

	var raw struct {
		Data struct {
			DeletedCount int64 `json:"deleted_count"`
		} `json:"data"`
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		// Body read failed after 200 — treat as 0 deleted, not fatal for DeleteByUser.
		return 0, nil
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		// Parse failed after 200 — treat as 0 deleted, not fatal for DeleteByUser.
		return 0, nil
	}
	return raw.Data.DeletedCount, nil
}

// ClearAll wipes all memories for the gojob user/cube via the MemDB bulk
// endpoint. Single round-trip, server-side handles SQL + Qdrant + VSET cleanup.
// Use this instead of ClearAllBySearch for full-cube rebuilds.
func (c *MemDBClient) ClearAll(ctx context.Context) error {
	body := map[string]any{
		"user_id": "gojob",
	}
	resp, err := engine.RetryHTTP(ctx, engine.DefaultRetryConfig, func() (*http.Response, error) {
		return c.post(ctx, "/product/delete_all_memories", body)
	})
	if err != nil {
		return fmt.Errorf("memdb clear_all: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("memdb clear_all: status %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// ClearAllBySearch iteratively searches and deletes all memories for gojob.
// Deprecated: prefer ClearAll for full-cube rebuilds — it uses a single bulk
// endpoint instead of a search loop. This method is retained for backwards
// compatibility but aborts after 2 consecutive iterations where deleted_count=0
// to prevent silent stuck-loop failures.
func (c *MemDBClient) ClearAllBySearch(ctx context.Context) error {
	consecutiveZeroDeletes := 0
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
		deleted, err := c.deleteByUserWithCount(ctx, ids)
		if err != nil {
			return fmt.Errorf("memdb clear delete: %w", err)
		}
		if deleted == 0 {
			consecutiveZeroDeletes++
			if consecutiveZeroDeletes >= 2 {
				return fmt.Errorf("memdb clear: stuck loop — search returns IDs but deleted_count=0 across %d iterations; use ClearAll() instead", consecutiveZeroDeletes)
			}
		} else {
			consecutiveZeroDeletes = 0
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

	return c.http.Do(req) //nolint:gosec // MemDB internal API URL, intentional outbound request
}
