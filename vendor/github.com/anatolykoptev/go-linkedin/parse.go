package linkedin

import (
	"encoding/json"
	"fmt"
	"strings"
)

type voyagerResponse struct {
	Data     json.RawMessage   `json:"data"`
	Included []json.RawMessage `json:"included"`
}

func parseVoyagerResponse(body []byte) (*voyagerResponse, error) {
	var resp voyagerResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse voyager response: %w", err)
	}
	return &resp, nil
}

func includedByType(included []json.RawMessage, typePrefix string) []json.RawMessage {
	var result []json.RawMessage
	for _, raw := range included {
		var peek struct {
			Type string `json:"$type"`
		}
		if json.Unmarshal(raw, &peek) == nil && strings.HasPrefix(peek.Type, typePrefix) {
			result = append(result, raw)
		}
	}
	return result
}
