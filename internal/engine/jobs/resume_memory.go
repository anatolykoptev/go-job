package jobs

import (
	"context"
	"errors"
	"strconv"
)

const (
	defaultTopK       = 10
	maxTopK           = 30
	searchRelativity  = 0.5
	defaultMemoryType = "note"
)

// --- Search ---

// ResumeMemoryItem is a single result from MemDB search.
type ResumeMemoryItem struct {
	Content  string  `json:"content"`
	Score    float64 `json:"score"`
	Type     string  `json:"type,omitempty"`
	ID       int     `json:"id,omitempty"`
	MemoryID string  `json:"memory_id"`
}

// ResumeMemorySearchResult is the output of resume_memory_search.
type ResumeMemorySearchResult struct {
	Query   string             `json:"query"`
	Results []ResumeMemoryItem `json:"results"`
	Total   int                `json:"total"`
}

// SearchResumeMemory queries MemDB for resume-related vectors.
func SearchResumeMemory(ctx context.Context, query string, topK int) (*ResumeMemorySearchResult, error) {
	mdb := GetMemDB()
	if mdb == nil {
		return nil, errors.New("MemDB not configured (set MEMDB_URL)")
	}

	if topK <= 0 {
		topK = defaultTopK
	}
	if topK > maxTopK {
		topK = maxTopK
	}

	results, err := mdb.Search(ctx, query, topK, searchRelativity)
	if err != nil {
		return nil, err
	}

	items := make([]ResumeMemoryItem, 0, len(results))
	for _, r := range results {
		item := ResumeMemoryItem{
			Content:  r.Content,
			Score:    r.Score,
			MemoryID: r.MemoryID,
		}
		if t, ok := r.Info["type"].(string); ok {
			item.Type = t
		}
		if idVal, ok := r.Info["id"]; ok {
			switch v := idVal.(type) {
			case float64:
				item.ID = int(v)
			case int:
				item.ID = v
			case int64:
				item.ID = int(v)
			case string:
				item.ID, _ = strconv.Atoi(v)
			}
		}
		items = append(items, item)
	}

	return &ResumeMemorySearchResult{
		Query:   query,
		Results: items,
		Total:   len(items),
	}, nil
}

// --- Add ---

// ResumeMemoryAddResult is the output of resume_memory_add.
type ResumeMemoryAddResult struct {
	Status string `json:"status"`
	Type   string `json:"type"`
}

// AddResumeMemory stores a new memory in MemDB.
func AddResumeMemory(ctx context.Context, content, memType string) (*ResumeMemoryAddResult, error) {
	mdb := GetMemDB()
	if mdb == nil {
		return nil, errors.New("MemDB not configured (set MEMDB_URL)")
	}

	if memType == "" {
		memType = defaultMemoryType
	}

	info := map[string]any{
		"type":   memType,
		"source": "agent",
	}

	if _, err := mdb.Add(ctx, content, info); err != nil {
		return nil, err
	}

	return &ResumeMemoryAddResult{
		Status: "stored",
		Type:   memType,
	}, nil
}

// --- Update (delete + re-add) ---

// ResumeMemoryUpdateResult is the output of resume_memory_update.
type ResumeMemoryUpdateResult struct {
	MemoryID string `json:"memory_id"`
	Updated  bool   `json:"updated"`
}

// UpdateResumeMemory replaces an existing memory by deleting and re-adding.
func UpdateResumeMemory(ctx context.Context, memoryID, content string) (*ResumeMemoryUpdateResult, error) {
	mdb := GetMemDB()
	if mdb == nil {
		return nil, errors.New("MemDB not configured (set MEMDB_URL)")
	}

	// Look up the old memory's type via a broad search and match by memoryID.
	// We use a generic query (not the new content) so the search actually finds
	// the old entry regardless of how different the new content is.
	var oldType string
	results, err := mdb.Search(ctx, "resume experience project skill achievement note goal", maxTopK, 0.0)
	if err == nil {
		for _, r := range results {
			if r.MemoryID == memoryID {
				if t, ok := r.Info["type"].(string); ok {
					oldType = t
				}
				break
			}
		}
	}
	if oldType == "" {
		oldType = defaultMemoryType
	}

	// Delete old memory
	if err := mdb.DeleteByUser(ctx, []string{memoryID}); err != nil {
		return nil, err
	}

	// Re-add with same type
	info := map[string]any{
		"type":   oldType,
		"source": "agent",
	}
	addResult, err := mdb.Add(ctx, content, info)
	if err != nil {
		return nil, err
	}

	// Return the new memory_id (old one no longer exists after delete).
	newID := memoryID
	if addResult != nil && addResult.MemoryID != "" {
		newID = addResult.MemoryID
	}

	return &ResumeMemoryUpdateResult{
		MemoryID: newID,
		Updated:  true,
	}, nil
}
