package linkedin

import (
	"context"
	"encoding/json"
	"fmt"
)

func (c *Client) GetSkills(ctx context.Context, profileID string) ([]Skill, error) {
	endpoint := fmt.Sprintf("/voyager/api/identity/profiles/%s/skillCategory", profileID)
	body, err := c.do(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("get skills: %w", err)
	}
	resp, err := parseVoyagerResponse(body)
	if err != nil {
		return nil, err
	}
	items := includedByType(resp.Included, "com.linkedin.voyager.dash.identity.profile.Skill")
	var skills []Skill
	for _, raw := range items {
		var s struct {
			Name             string `json:"name"`
			EndorsementCount int    `json:"endorsementCount"`
		}
		if json.Unmarshal(raw, &s) == nil && s.Name != "" {
			skills = append(skills, Skill{Name: s.Name, EndorsementCount: s.EndorsementCount})
		}
	}
	return skills, nil
}
