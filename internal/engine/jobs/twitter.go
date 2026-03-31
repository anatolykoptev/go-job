package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	twitter "github.com/anatolykoptev/go-twitter"
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

const jobSearchTerms = `hiring OR job OR career OR vacancy`

func isJobQuery(q string) bool {
	lower := strings.ToLower(q)
	for _, term := range []string{"hiring", "job", "career", "vacancy", "recruit", "looking for"} {
		if strings.Contains(lower, term) {
			return true
		}
	}
	return false
}

func buildTwitterJobQuery(query string) string {
	if isJobQuery(query) {
		return query
	}
	return query + " " + jobSearchTerms
}

// searchViaSocial acquires an account from go-social, searches, and reports back.
func searchViaSocial(ctx context.Context, query string, limit int) ([]*twitter.Tweet, error) {
	sc := engine.Cfg.SocialClient
	if sc == nil {
		return nil, errors.New("social client not configured")
	}

	creds, err := sc.AcquireAccount(ctx, "twitter")
	if err != nil {
		return nil, fmt.Errorf("acquire account: %w", err)
	}

	acc := &twitter.Account{
		Username:  creds.Credentials["username"],
		AuthToken: creds.Credentials["auth_token"],
		CT0:       creds.Credentials["ct0"],
		Proxy:     creds.Proxy,
	}

	tw, err := twitter.NewClient(twitter.ClientConfig{
		Accounts: []*twitter.Account{acc},
	})
	if err != nil {
		_ = sc.ReportUsage(ctx, "twitter", creds.ID, "auth_error")
		return nil, fmt.Errorf("create ephemeral client: %w", err)
	}

	tweets, err := tw.SearchTimeline(ctx, query, limit)
	if err != nil {
		_ = sc.ReportUsage(ctx, "twitter", creds.ID, "auth_error")
		return nil, fmt.Errorf("social search: %w", err)
	}

	_ = sc.ReportUsage(ctx, "twitter", creds.ID, "success")
	return tweets, nil
}

// searchTwitter tries go-social first, falls back to local twitter client.
// Fallback to local only happens when SocialClient is not configured at all.
func searchTwitter(ctx context.Context, query string, limit int) ([]*twitter.Tweet, error) {
	// Try go-social pool first
	if engine.Cfg.SocialClient != nil {
		tweets, err := searchViaSocial(ctx, query, limit)
		if err == nil {
			slog.Info("twitter search via go-social", slog.Int("tweets", len(tweets)))
			return tweets, nil
		}
		// Social is configured but failed — try local if available, else return social error
		tw := engine.Cfg.TwitterClient
		if tw != nil {
			slog.Warn("go-social search failed, trying local", slog.Any("error", err))
			return tw.SearchTimeline(ctx, query, limit)
		}
		return nil, err
	}

	// No social client — use local twitter client directly
	tw := engine.Cfg.TwitterClient
	if tw == nil {
		return nil, errors.New("twitter not configured: no go-social and no local client")
	}
	return tw.SearchTimeline(ctx, query, limit)
}

// SearchTwitterJobs searches Twitter for job-related tweets and converts them to SearxngResult.
func SearchTwitterJobs(ctx context.Context, query string, limit int) ([]engine.SearxngResult, error) {
	twitterQuery := buildTwitterJobQuery(query)
	if limit <= 0 {
		limit = 30
	}

	tweets, err := searchTwitter(ctx, twitterQuery, limit)
	if err != nil {
		return nil, fmt.Errorf("twitter search: %w", err)
	}

	slog.Info("twitter job search", slog.Int("tweets", len(tweets)), slog.String("query", twitterQuery))

	results := make([]engine.SearxngResult, 0, len(tweets))
	for _, t := range tweets {
		tweetURL := "https://x.com/i/status/" + t.ID
		lines := strings.SplitN(strings.TrimSpace(t.Text), "\n", 2)
		title := lines[0]
		if len(title) > 120 {
			title = title[:117] + "..."
		}
		content := fmt.Sprintf("**Author:** %s | **Likes:** %d | **RT:** %d\n\n%s",
			t.AuthorID, t.Likes, t.Retweets, t.Text)
		results = append(results, engine.SearxngResult{
			Title: title, Content: content, URL: tweetURL, Score: 0,
		})
	}
	return results, nil
}

// SearchTwitterJobsRaw searches Twitter for job-related tweets and returns raw tweet data.
func SearchTwitterJobsRaw(ctx context.Context, query string, limit int) ([]TwitterJobTweet, error) {
	twitterQuery := buildTwitterJobQuery(query)
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}

	tweets, err := searchTwitter(ctx, twitterQuery, limit)
	if err != nil {
		return nil, fmt.Errorf("twitter search: %w", err)
	}

	slog.Info("twitter job search raw", slog.Int("tweets", len(tweets)), slog.String("query", twitterQuery))

	result := make([]TwitterJobTweet, 0, len(tweets))
	for _, t := range tweets {
		result = append(result, TwitterJobTweet{
			ID: t.ID, AuthorID: t.AuthorID, Text: t.Text,
			URL:       "https://x.com/i/status/" + t.ID,
			Likes:     t.Likes,
			Retweets:  t.Retweets,
			CreatedAt: t.CreatedAt.Format("2006-01-02T15:04:05Z"),
		})
	}
	return result, nil
}
