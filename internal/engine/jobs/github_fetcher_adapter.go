package jobs

import (
	"context"
	"errors"
	"log/slog"

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
//
// Returns an error when GITHUB_TOKEN is unset — the caller (EnrichBountyStatusGitHub)
// interprets this as a hard failure and does NOT bump last_checked_at, preventing a
// tight loop where every ListBounties call re-enqueues the same unchecked rows.
func (a *GithubFetcherAdapter) FetchIssueInfoBatch(ctx context.Context, urls []string) (map[string]enrich.GithubIssueInfo, error) {
	if engine.Cfg.GithubToken == "" {
		slog.Warn("github_fetcher_adapter: GITHUB_TOKEN unset; status enrichment disabled")
		return nil, errors.New("github token unavailable")
	}

	// Convert URLs to minimal BountyListing structs (only URL is used by fetchIssueInfoBatch).
	bounties := make([]engine.BountyListing, len(urls))
	for i, u := range urls {
		bounties[i] = engine.BountyListing{URL: u}
	}

	raw := fetchIssueInfoBatch(ctx, bounties)

	out := make(map[string]enrich.GithubIssueInfo, len(raw))
	for u, info := range raw {
		// state_reason="completed" means the issue was closed as done (PR merged / bounty claimed).
		// state_reason="not_planned" means declined/won't-fix — not a successful claim.
		merged := info.State == statusClosed && info.StateReason == "completed"
		out[u] = enrich.GithubIssueInfo{
			Title:  info.Title,
			State:  info.State,
			Merged: merged,
			Labels: info.Labels,
		}
	}
	return out, nil
}
