package jobs

import (
	"context"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/hunt/enrich"
)

// GithubFetcherAdapter wraps the package-level fetchIssueInfoBatch function,
// adapting it to the enrich.GithubIssueFetcher interface.
// Constructed once in main.go and set on the hunt store via hStore.SetEnricher.
type GithubFetcherAdapter struct{}

// NewGithubFetcherAdapter returns an adapter that satisfies enrich.GithubIssueFetcher.
func NewGithubFetcherAdapter() *GithubFetcherAdapter {
	return &GithubFetcherAdapter{}
}

// FetchIssueInfoBatch implements enrich.GithubIssueFetcher.
// It wraps the existing fetchIssueInfoBatch which expects []engine.BountyListing
// and maps the result to enrich.GithubIssueInfo.
func (a *GithubFetcherAdapter) FetchIssueInfoBatch(ctx context.Context, urls []string) (map[string]enrich.GithubIssueInfo, error) {
	// Convert URLs to minimal BountyListing structs (only URL is used by fetchIssueInfoBatch).
	bounties := make([]engine.BountyListing, len(urls))
	for i, u := range urls {
		bounties[i] = engine.BountyListing{URL: u}
	}

	raw := fetchIssueInfoBatch(ctx, bounties)

	out := make(map[string]enrich.GithubIssueInfo, len(raw))
	for u, info := range raw {
		out[u] = enrich.GithubIssueInfo{
			Title:  info.Title,
			State:  info.State,
			Merged: false, // GitHub Issues API doesn't expose PR merged status directly;
			// closed issues without a linked merged PR are StatusClosed.
			// A future enhancement can add a PR check here.
			Labels: info.Labels,
		}
	}
	return out, nil
}
