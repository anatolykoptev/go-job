package linkedin

import "context"

const meEndpoint = "/voyager/api/me"

type Me struct {
	URN       string `json:"entityUrn"`
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
}

func (c *Client) GetMe(ctx context.Context) (*Me, error) {
	body, err := c.do(ctx, meEndpoint)
	if err != nil {
		return nil, err
	}
	resp, err := parseVoyagerResponse(body)
	if err != nil {
		return nil, err
	}
	var me Me
	if err := safeUnmarshal(resp.Data, &me); err != nil {
		return nil, err
	}
	return &me, nil
}
