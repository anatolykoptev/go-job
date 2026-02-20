package engine

// HN Algolia API shared constants and types.
// Used by both sources/hackernews.go (HN search) and jobs/hnjobs.go (Who is Hiring).

const HNAlgoliaURL = "https://hn.algolia.com/api/v1/search"
const HNAlgoliaByDateURL = "https://hn.algolia.com/api/v1/search_by_date"

// HNAlgoliaResponse is the response from the Hacker News Algolia API.
type HNAlgoliaResponse struct {
	Hits   []HNHit `json:"hits"`
	NbHits int     `json:"nbHits"`
}

// HNHit is a single result from the HN Algolia API (story or comment).
type HNHit struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Author      string `json:"author"`
	Points      int    `json:"points"`
	NumComments int    `json:"num_comments"`
	CreatedAtI  int64  `json:"created_at_i"`
	StoryText   string `json:"story_text"`
	CommentText string `json:"comment_text"`
	ObjectID    string `json:"objectID"`
	StoryID     *int64 `json:"story_id"`
	ParentID    *int64 `json:"parent_id"`
}
