package linkedin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

func (c *Client) SearchPeople(ctx context.Context, params SearchParams) ([]SearchResult, error) {
	return c.search(ctx, params, "PEOPLE")
}

func (c *Client) SearchCompanies(ctx context.Context, params SearchParams) ([]SearchResult, error) {
	return c.search(ctx, params, "COMPANIES")
}

func (c *Client) search(ctx context.Context, params SearchParams, vertical string) ([]SearchResult, error) {
	if params.Limit <= 0 {
		params.Limit = 10
	}
	q := url.Values{}
	q.Set("q", "all")
	q.Set("keywords", params.Query)
	q.Set("filters", fmt.Sprintf("List(resultType->%s)", vertical))
	q.Set("count", fmt.Sprintf("%d", params.Limit))
	endpoint := "/voyager/api/search/dash/clusters?" + q.Encode()
	body, err := c.do(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("search %s: %w", vertical, err)
	}
	resp, err := parseVoyagerResponse(body)
	if err != nil {
		return nil, err
	}
	typePrefix := "com.linkedin.voyager.dash.search.EntityResultViewModel"
	items := includedByType(resp.Included, typePrefix)
	var results []SearchResult
	for _, raw := range items {
		var r struct {
			EntityURN string `json:"entityUrn"`
			Title     struct {
				Text string `json:"text"`
			} `json:"title"`
			Summary struct {
				Text string `json:"text"`
			} `json:"primarySubtitle"`
			Location struct {
				Text string `json:"text"`
			} `json:"secondarySubtitle"`
		}
		if json.Unmarshal(raw, &r) != nil || r.Title.Text == "" {
			continue
		}
		results = append(results, SearchResult{
			URN:      r.EntityURN,
			Name:     r.Title.Text,
			Headline: r.Summary.Text,
			Location: r.Location.Text,
		})
	}
	return results, nil
}
