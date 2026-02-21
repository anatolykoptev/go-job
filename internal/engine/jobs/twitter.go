package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// TwitterJobTweet is a raw tweet from a Twitter job search.
type TwitterJobTweet struct {
	ID        string `json:"id"`
	AuthorID  string `json:"author_id"`
	Text      string `json:"text"`
	URL       string `json:"url"`
	Likes     int    `json:"likes"`
	Retweets  int    `json:"retweets"`
	CreatedAt string `json:"created_at"`
}

// jobSearchTerms are appended to queries to find job-related tweets.
const jobSearchTerms = `hiring OR job OR career OR vacancy`

// isJobQuery returns true if the query already contains job-related terms.
func isJobQuery(q string) bool {
	lower := strings.ToLower(q)
	for _, term := range []string{"hiring", "job", "career", "vacancy", "recruit", "looking for"} {
		if strings.Contains(lower, term) {
			return true
		}
	}
	return false
}

// buildTwitterJobQuery enhances a query with job-related terms if needed.
func buildTwitterJobQuery(query string) string {
	if isJobQuery(query) {
		return query
	}
	return query + " " + jobSearchTerms
}

// SearchTwitterJobs searches Twitter for job-related tweets and converts them to SearxngResult.
func SearchTwitterJobs(ctx context.Context, query string, limit int) ([]engine.SearxngResult, error) {
	tw := engine.Cfg.TwitterClient
	if tw == nil {
		return nil, errors.New("twitter client not configured")
	}

	twitterQuery := buildTwitterJobQuery(query)
	if limit <= 0 {
		limit = 30
	}

	tweets, err := tw.SearchTimeline(ctx, twitterQuery, limit)
	if err != nil {
		return nil, fmt.Errorf("twitter search: %w", err)
	}

	slog.Info("twitter job search", slog.Int("tweets", len(tweets)), slog.String("query", twitterQuery))

	results := make([]engine.SearxngResult, 0, len(tweets))
	for _, t := range tweets {
		tweetURL := "https://x.com/i/status/" + t.ID

		// First line as title, rest as content
		lines := strings.SplitN(strings.TrimSpace(t.Text), "\n", 2)
		title := lines[0]
		if len(title) > 120 {
			title = title[:117] + "..."
		}

		content := fmt.Sprintf("**Author:** %s | **Likes:** %d | **RT:** %d\n\n%s",
			t.AuthorID, t.Likes, t.Retweets, t.Text)

		results = append(results, engine.SearxngResult{
			Title:   title,
			Content: content,
			URL:     tweetURL,
			Score:   0,
		})
	}
	return results, nil
}

// SearchTwitterJobsRaw searches Twitter for job-related tweets and returns raw tweet data.
func SearchTwitterJobsRaw(ctx context.Context, query string, limit int) ([]TwitterJobTweet, error) {
	tw := engine.Cfg.TwitterClient
	if tw == nil {
		return nil, errors.New("twitter client not configured")
	}

	twitterQuery := buildTwitterJobQuery(query)
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}

	tweets, err := tw.SearchTimeline(ctx, twitterQuery, limit)
	if err != nil {
		return nil, fmt.Errorf("twitter search: %w", err)
	}

	slog.Info("twitter job search raw", slog.Int("tweets", len(tweets)), slog.String("query", twitterQuery))

	result := make([]TwitterJobTweet, 0, len(tweets))
	for _, t := range tweets {
		result = append(result, TwitterJobTweet{
			ID:        t.ID,
			AuthorID:  t.AuthorID,
			Text:      t.Text,
			URL:       "https://x.com/i/status/" + t.ID,
			Likes:     t.Likes,
			Retweets:  t.Retweets,
			CreatedAt: t.CreatedAt.Format("2006-01-02T15:04:05Z"),
		})
	}
	return result, nil
}
