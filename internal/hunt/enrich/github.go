// Package enrich provides lazy on-read GitHub issue status enrichment for hunt entries.
// It is a separate package from hunt to keep the store import-cycle free.
package enrich

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/anatolykoptev/go_job/internal/hunt"
)

// GithubIssueInfo holds enrichment data fetched from the GitHub API.
type GithubIssueInfo struct {
	Title  string
	State  string // "open" or "closed"
	Merged bool   // true when closed and the linked PR was merged (bounty claimed)
	Labels []string
}

// GithubIssueFetcher abstracts the existing fetchIssueInfoBatch call for testability.
type GithubIssueFetcher interface {
	FetchIssueInfoBatch(ctx context.Context, urls []string) (map[string]GithubIssueInfo, error)
}

// StatusUpdater is the store interface needed by this package; mirrors hunt.StatusUpdater.
type StatusUpdater interface {
	UpdateStatus(ctx context.Context, kind string, id int64, status string, closedAt *time.Time) error
}

// EnrichBountyStatusGitHub filters entries for GitHub issue URLs that are overdue
// for a status check (based on maxAge), does a parallel batch fetch, and calls
// store.UpdateStatus for any issue whose state has changed to closed/merged.
//
// It is designed to run in a goroutine spawned by Store.ListBounties — it must
// NOT block the caller, must NOT panic on fetch failure, and should be idempotent.
func EnrichBountyStatusGitHub(ctx context.Context, store StatusUpdater, fetcher GithubIssueFetcher, entries []hunt.Bounty, maxAge time.Duration) {
	var urls []string
	indexByURL := make(map[string]int64, len(entries))
	cutoff := time.Now().Add(-maxAge)

	for _, e := range entries {
		if e.Status != hunt.StatusOpen {
			continue // already closed — nothing to re-check
		}
		if e.LastCheckedAt != nil && e.LastCheckedAt.After(cutoff) {
			continue // checked recently enough
		}
		if !isGithubIssueURL(e.URL) {
			continue
		}
		urls = append(urls, e.URL)
		indexByURL[e.URL] = e.ID
	}

	if len(urls) == 0 {
		return
	}

	info, err := fetcher.FetchIssueInfoBatch(ctx, urls)
	if err != nil {
		slog.Warn("hunt enrich: batch fetch failed", slog.Any("error", err))
		return
	}

	for url, id := range indexByURL {
		i, ok := info[url]
		if !ok {
			continue
		}

		newStatus := hunt.StatusOpen
		var closedAt *time.Time

		if i.State == "closed" {
			now := time.Now()
			closedAt = &now
			if i.Merged {
				newStatus = hunt.StatusMerged
			} else {
				newStatus = hunt.StatusClosed
			}
		}

		// For still-open entries, call UpdateStatus anyway so last_checked_at is bumped.
		if err := store.UpdateStatus(ctx, hunt.KindBounty, id, newStatus, closedAt); err != nil {
			slog.Warn("hunt enrich: update status failed",
				slog.Int64("id", id), slog.Any("error", err))
		}
	}
}

// isGithubIssueURL returns true for URLs that look like GitHub issue links.
func isGithubIssueURL(u string) bool {
	return strings.Contains(u, "github.com/") && strings.Contains(u, "/issues/")
}

// Enricher implements hunt.BountyEnricher. It wires to a GithubIssueFetcher to
// call EnrichBountyStatusGitHub. Constructed in internal/engine/jobs and set on the store.
type Enricher struct {
	fetcher GithubIssueFetcher
}

// NewEnricher wraps a GithubIssueFetcher for use as a hunt.BountyEnricher.
func NewEnricher(f GithubIssueFetcher) *Enricher {
	return &Enricher{fetcher: f}
}

// EnrichBountyStatus implements hunt.BountyEnricher.
func (e *Enricher) EnrichBountyStatus(ctx context.Context, store hunt.StatusUpdater, entries []hunt.Bounty, maxAge time.Duration) {
	EnrichBountyStatusGitHub(ctx, store, e.fetcher, entries, maxAge)
}
