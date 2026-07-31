package jobs

// opportunity_search_algora_test.go guards the removal of the algora bounty
// source from the scheduled/on-demand bounty fan-out (fetchAllBountiesImpl).
//
// Background: algora's public bounty product is gone. The tRPC endpoint
// (console.algora.io/api/trpc/bounty.list) answers HTTP 200 with items:[]
// (zero bounties), and the HTML scrape URL (algora.io/bounties) returns 404.
// Both paths failed silently every ingest cycle. On 2026-07-30 algora was
// removed from fetchAllBountiesImpl rather than left failing in the rotation.
//
// Falsification strategy: the algora enrichment cache (algoraEnrichedCacheKey)
// is pre-seeded with a known algora bounty. SearchAlgoraEnriched returns cached
// bounties WITHOUT any network call, so if the fan-out still called it the
// algora bounty would appear in the output. The other 5 bounty sources are
// forced to fail fast via a failing HTTPClient (no real network). The test
// asserts NO bounty in the fan-out output has Source == sourceAlgora.
//
// Revert-red: re-add the `SearchAlgoraEnriched(ctx, limit)` block to
// fetchAllBountiesImpl → the cached algora bounty leaks into the output → test
// fails with "algora bounty leaked into fan-out output".

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// errTransport makes every outbound HTTP request fail immediately so the
// non-algora bounty sources (opire/bountyhub/boss/lightning/collaborators)
// contribute nothing and never touch real network. The algora path, if still
// wired, returns from the pre-seeded cache without using HTTPClient.
type errTransport struct{}

func (errTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("test: no network")
}

// TestFetchAllBountiesImpl_AlgoraRemovedFromFanOut asserts the bounty fan-out
// no longer references algora, by proving a cached algora bounty does NOT
// appear in the output even though it would be served cache-only (no network)
// if the fan-out still called SearchAlgoraEnriched.
func TestFetchAllBountiesImpl_AlgoraRemovedFromFanOut(t *testing.T) {
	// In-memory L1 cache (L2 disabled via empty redisURL) so the algora
	// enriched cache can be pre-seeded without engine.Init / Redis.
	// Save/restore the process-global cache so later tests in the package
	// don't inherit a fresh cache (ordering-dependent coupling).
	origCacheTTL := engine.CacheTTL
	engine.InitCache("", 5*time.Minute, 100, 0)
	t.Cleanup(func() {
		engine.CacheTTL = origCacheTTL
		engine.InitCache("", origCacheTTL, 100, 0)
	})

	// Pre-seed the algora enriched cache with a known algora bounty.
	algoraBounty := BountyWithVector{Bounty: engine.BountyListing{
		Title:  "Test Algora Bounty (should NOT appear)",
		Org:    "test/repo",
		URL:    "https://github.com/test/repo/issues/1",
		Amount: "$500",
		Source: sourceAlgora,
	}}
	engine.CacheStoreJSON(context.Background(), algoraEnrichedCacheKey, "",
		[]BountyWithVector{algoraBounty})

	// Force the other 5 bounty sources to fail fast without real network.
	origClient := engine.Cfg.HTTPClient
	origTimeout := engine.Cfg.FetchTimeout
	engine.Cfg.HTTPClient = &http.Client{Transport: errTransport{}}
	engine.Cfg.FetchTimeout = 1 * time.Second
	t.Cleanup(func() {
		engine.Cfg.HTTPClient = origClient
		engine.Cfg.FetchTimeout = origTimeout
	})

	bounties := fetchAllBountiesImpl(context.Background(), 50, true)

	for _, b := range bounties {
		if b.Source == sourceAlgora {
			t.Fatalf("algora bounty leaked into fan-out output; algora must be removed from fetchAllBountiesImpl. got: %+v", b)
		}
	}
}
