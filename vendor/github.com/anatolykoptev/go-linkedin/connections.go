package linkedin

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// GetConnections fetches the connections of a profile.
// NOTE: Voyager API only returns connections for the authenticated user's own profile.
// For other profiles this returns empty or 403.
func (c *Client) GetConnections(ctx context.Context, profileID string, limit int) ([]Connection, error) {
	if limit <= 0 {
		limit = 50
	}
	endpoint := fmt.Sprintf("/voyager/api/relationships/dash/connections?q=search&sortType=RECENTLY_ADDED&count=%d&memberIdentity=%s", limit, profileID)
	body, err := c.do(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("get connections: %w", err)
	}
	resp, err := parseVoyagerResponse(body)
	if err != nil {
		return nil, err
	}
	// Build URN→member lookup for O(1) resolution
	memberMap := buildMemberMap(resp.Included)
	items := includedByType(resp.Included, "com.linkedin.voyager.dash.relationships.Connection")
	var conns []Connection
	for _, raw := range items {
		var conn struct {
			EntityURN   string `json:"entityUrn"`
			ConnectedAt int64  `json:"createdAt"`
			MemberURN   string `json:"connectedMemberResolutionResult"`
		}
		if json.Unmarshal(raw, &conn) != nil {
			continue
		}
		name, headline := "", ""
		if m, ok := memberMap[conn.MemberURN]; ok {
			name = m.name
			headline = m.headline
		}
		conns = append(conns, Connection{
			ProfileURN:  conn.MemberURN,
			Name:        name,
			Headline:    headline,
			ConnectedAt: time.UnixMilli(conn.ConnectedAt),
		})
	}
	return conns, nil
}

type memberInfo struct {
	name     string
	headline string
}

func buildMemberMap(included []json.RawMessage) map[string]memberInfo {
	m := make(map[string]memberInfo)
	for _, raw := range included {
		var member struct {
			EntityURN string `json:"entityUrn"`
			FirstName string `json:"firstName"`
			LastName  string `json:"lastName"`
			Headline  string `json:"headline"`
		}
		if json.Unmarshal(raw, &member) == nil && member.FirstName != "" {
			m[member.EntityURN] = memberInfo{
				name:     member.FirstName + " " + member.LastName,
				headline: member.Headline,
			}
		}
	}
	return m
}

func (c *Client) GetMutualConnections(ctx context.Context, profileID1, profileID2 string) ([]Connection, error) {
	endpoint := fmt.Sprintf("/voyager/api/search/dash/clusters?q=mutual&memberIdentity=%s&pivotMemberIdentity=%s&count=20", profileID1, profileID2)
	body, err := c.do(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("get mutual connections: %w", err)
	}
	resp, err := parseVoyagerResponse(body)
	if err != nil {
		return nil, err
	}
	typePrefix := "com.linkedin.voyager.dash.search.EntityResultViewModel"
	items := includedByType(resp.Included, typePrefix)
	var conns []Connection
	for _, raw := range items {
		var r struct {
			EntityURN string `json:"entityUrn"`
			Title     struct {
				Text string `json:"text"`
			} `json:"title"`
			Summary struct {
				Text string `json:"text"`
			} `json:"primarySubtitle"`
		}
		if json.Unmarshal(raw, &r) != nil || r.Title.Text == "" {
			continue
		}
		conns = append(conns, Connection{
			ProfileURN: r.EntityURN,
			Name:       r.Title.Text,
			Headline:   r.Summary.Text,
		})
	}
	return conns, nil
}
