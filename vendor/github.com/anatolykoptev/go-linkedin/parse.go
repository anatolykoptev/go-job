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

// extractTargetURN extracts the target profile URN from the response data field.
// The data field contains {"*elements": ["urn:li:fsd_profile:ACoAAA..."], ...}.
func extractTargetURN(data json.RawMessage) string {
	if len(data) == 0 {
		return ""
	}
	var d struct {
		Elements []string `json:"*elements"`
	}
	if json.Unmarshal(data, &d) != nil || len(d.Elements) == 0 {
		return ""
	}
	return d.Elements[0]
}

// findProfileByURN finds the Profile item in included[] matching the given entityUrn.
// Falls back to the first Profile-type item if urn is empty (backward compat).
func findProfileByURN(included []json.RawMessage, urn string) json.RawMessage {
	const profileType = "com.linkedin.voyager.dash.identity.profile.Profile"
	var firstProfile json.RawMessage
	for _, raw := range included {
		var peek struct {
			Type      string `json:"$type"`
			EntityURN string `json:"entityUrn"`
		}
		if json.Unmarshal(raw, &peek) != nil || !strings.HasPrefix(peek.Type, profileType) {
			continue
		}
		if firstProfile == nil {
			firstProfile = raw
		}
		if urn != "" && peek.EntityURN == urn {
			return raw
		}
	}
	if urn == "" {
		return firstProfile
	}
	return firstProfile // fallback if URN not found in included
}
