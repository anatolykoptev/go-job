package linkedin

import (
	"context"
	"encoding/json"
	"fmt"
)

func (c *Client) GetContactInfo(ctx context.Context, profileID string) (*ContactInfo, error) {
	endpoint := fmt.Sprintf("/voyager/api/identity/profiles/%s/profileContactInfo", profileID)
	body, err := c.do(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("get contact info: %w", err)
	}
	var raw struct {
		Data struct {
			EmailAddress string `json:"emailAddress"`
			PhoneNumbers []struct {
				Number string `json:"number"`
			} `json:"phoneNumbers"`
			TwitterHandles []struct {
				Name string `json:"name"`
			} `json:"twitterHandles"`
			Websites []struct {
				URL string `json:"url"`
			} `json:"websites"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse contact info: %w", err)
	}
	info := &ContactInfo{
		Email: raw.Data.EmailAddress,
	}
	if len(raw.Data.PhoneNumbers) > 0 {
		info.Phone = raw.Data.PhoneNumbers[0].Number
	}
	if len(raw.Data.TwitterHandles) > 0 {
		info.Twitter = raw.Data.TwitterHandles[0].Name
	}
	for _, w := range raw.Data.Websites {
		info.Websites = append(info.Websites, w.URL)
	}
	return info, nil
}
