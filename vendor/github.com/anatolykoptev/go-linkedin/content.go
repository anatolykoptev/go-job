package linkedin

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

func (c *Client) GetPosts(ctx context.Context, profileID string, limit int) ([]Post, error) {
	if limit <= 0 {
		limit = 10
	}
	endpoint := fmt.Sprintf("/voyager/api/feed/updates?profileId=%s&q=memberShareFeed&count=%d", profileID, limit)
	body, err := c.do(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("get posts: %w", err)
	}
	resp, err := parseVoyagerResponse(body)
	if err != nil {
		return nil, err
	}
	items := includedByType(resp.Included, "com.linkedin.voyager.feed.render.UpdateV2")
	var posts []Post
	for _, raw := range items {
		var update struct {
			URN        string `json:"urn"`
			ActorURN   string `json:"actorUrn"`
			Commentary *struct {
				Text struct {
					Text string `json:"text"`
				} `json:"text"`
			} `json:"commentary"`
			SocialDetail *struct {
				TotalSocialActivityCounts *struct {
					NumLikes    int `json:"numLikes"`
					NumComments int `json:"numComments"`
					NumShares   int `json:"numShares"`
				} `json:"totalSocialActivityCounts"`
			} `json:"socialDetail"`
			PublishedAt int64 `json:"publishedAt"`
		}
		if json.Unmarshal(raw, &update) != nil {
			continue
		}
		post := Post{
			URN:         update.URN,
			AuthorURN:   update.ActorURN,
			PublishedAt: time.UnixMilli(update.PublishedAt),
		}
		if update.Commentary != nil {
			post.Text = update.Commentary.Text.Text
		}
		if update.SocialDetail != nil && update.SocialDetail.TotalSocialActivityCounts != nil {
			counts := update.SocialDetail.TotalSocialActivityCounts
			post.Likes = counts.NumLikes
			post.Comments = counts.NumComments
			post.Reposts = counts.NumShares
		}
		if post.Text != "" || post.Likes > 0 {
			posts = append(posts, post)
		}
	}
	return posts, nil
}
