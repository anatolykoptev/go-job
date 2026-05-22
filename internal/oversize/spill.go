package oversize

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

// DefaultThresholdBytes is the size beyond which a payload is spilled to store.
const DefaultThresholdBytes = 24 * 1024

// Envelope is the small object returned to the MCP client when the payload spills.
type Envelope struct {
	OversizeID  int64           `json:"oversize_id"`
	ToolName    string          `json:"tool_name"`
	SizeBytes   int             `json:"size_bytes"`
	SHA256      string          `json:"sha256"`
	ItemCount   int             `json:"item_count,omitempty"`
	Sample      json.RawMessage `json:"sample,omitempty"`
	RetrieveVia string          `json:"retrieve_via"` // hint: "oversize_get(id)"
	Hint        string          `json:"hint"`
}

// Storer is the minimal interface MaybeSpill needs (for testability).
type Storer interface {
	Save(ctx context.Context, e Entry) (int64, error)
}

// thresholdBytes reads GO_JOB_OVERSIZE_THRESHOLD_BYTES env, falls back to default.
func thresholdBytes() int {
	if v := os.Getenv("GO_JOB_OVERSIZE_THRESHOLD_BYTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return DefaultThresholdBytes
}

// knownListKeys are map keys whose values, if slices, yield item count.
var knownListKeys = []string{"jobs", "bounties", "opportunities", "projects", "results", "items", "data"}

// itemCount tries to count the top-level array length of a payload:
//   - if payload is []any → returns len
//   - if payload is map[string]any with a known list key → returns len of that slice
//
// Returns 0 if the shape is unknown.
func itemCount(payload any) int {
	switch v := payload.(type) {
	case []any:
		return len(v)
	case map[string]any:
		for _, key := range knownListKeys {
			if raw, ok := v[key]; ok {
				if s, ok := raw.([]any); ok {
					return len(s)
				}
			}
		}
	}
	return 0
}

// firstN returns the first n items if payload is a slice-shaped value, else nil.
// The result is a marshalled JSON array of those items.
func firstN(payload any, n int) json.RawMessage {
	var items []any
	switch v := payload.(type) {
	case []any:
		items = v
	case map[string]any:
		for _, key := range knownListKeys {
			if raw, ok := v[key]; ok {
				if s, ok := raw.([]any); ok {
					items = s
					break
				}
			}
		}
	}
	if len(items) == 0 {
		return nil
	}
	if n > len(items) {
		n = len(items)
	}
	out, err := json.Marshal(items[:n])
	if err != nil {
		return nil
	}
	return out
}

// MaybeSpill serializes `payload` and decides:
//   - if marshalled length <= threshold → returns payload unchanged
//   - else → Save to store and return *Envelope
//
// Errors from json.Marshal or store.Save are returned; caller is responsible
// for fallback behaviour (likely returning original payload + log).
func MaybeSpill(ctx context.Context, store Storer, toolName string, payload any) (any, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("oversize: marshal: %w", err)
	}
	if len(raw) <= thresholdBytes() {
		return payload, nil
	}
	sum := sha256.Sum256(raw)
	sample := firstN(payload, 3)
	count := itemCount(payload)
	e := Entry{
		ToolName:  toolName,
		Payload:   raw,
		SizeBytes: len(raw),
		SHA256:    hex.EncodeToString(sum[:]),
		Sample:    sample,
		ItemCount: count,
	}
	id, err := store.Save(ctx, e)
	if err != nil {
		return nil, fmt.Errorf("oversize: save: %w", err)
	}
	return &Envelope{
		OversizeID:  id,
		ToolName:    toolName,
		SizeBytes:   len(raw),
		SHA256:      e.SHA256,
		ItemCount:   count,
		Sample:      sample,
		RetrieveVia: "oversize_get",
		Hint:        fmt.Sprintf("payload spilled to oversize store; call oversize_get with id=%d", id),
	}, nil
}
