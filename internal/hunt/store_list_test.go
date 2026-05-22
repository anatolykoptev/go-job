package hunt_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- BountyFilter.Skills wire ---

// TestStore_ListBounties_FilterBySkills verifies that skills GIN filter works.
func TestStore_ListBounties_FilterBySkills(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))
	truncateBounties(t, pool)

	_, _, err := s.UpsertBounty(ctx, hunt.Bounty{
		DedupHash: hunt.DedupHash("https://example.com/skills/1"),
		Title:     "Go bounty",
		URL:       "https://example.com/skills/1",
		Source:    "algora",
		Skills:    []string{"go", "postgres"},
	})
	require.NoError(t, err)

	_, _, err = s.UpsertBounty(ctx, hunt.Bounty{
		DedupHash: hunt.DedupHash("https://example.com/skills/2"),
		Title:     "Rust bounty",
		URL:       "https://example.com/skills/2",
		Source:    "algora",
		Skills:    []string{"rust"},
	})
	require.NoError(t, err)

	goBounties, err := s.ListBounties(ctx, hunt.BountyFilter{Skills: []string{"go"}, Limit: 10})
	require.NoError(t, err)
	assert.Len(t, goBounties, 1, "skills filter should return only bounties with 'go'")
	assert.Equal(t, "Go bounty", goBounties[0].Title)
}

// --- ListFreelance ---

// TestStore_ListFreelance_Basic verifies ListFreelance returns all rows when no filter.
func TestStore_ListFreelance_Basic(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))
	truncateFreelance(t, pool)

	_, _, err := s.UpsertFreelance(ctx, hunt.Freelance{
		DedupHash: hunt.DedupHash("https://upwork.com/fl/list/1"),
		Title:     "Build API",
		URL:       "https://upwork.com/fl/list/1",
		Platform:  "upwork",
		Source:    "upwork",
		Skills:    []string{"go"},
	})
	require.NoError(t, err)

	results, err := s.ListFreelance(ctx, hunt.FreelanceFilter{Limit: 10})
	require.NoError(t, err)
	assert.Len(t, results, 1)
}

// TestStore_ListFreelance_FilterBySkills verifies skills GIN filter on freelance.
func TestStore_ListFreelance_FilterBySkills(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))
	truncateFreelance(t, pool)

	_, _, err := s.UpsertFreelance(ctx, hunt.Freelance{
		DedupHash: hunt.DedupHash("https://upwork.com/fl/list/go"),
		Title:     "Go API",
		URL:       "https://upwork.com/fl/list/go",
		Platform:  "upwork",
		Source:    "upwork",
		Skills:    []string{"go"},
	})
	require.NoError(t, err)

	_, _, err = s.UpsertFreelance(ctx, hunt.Freelance{
		DedupHash: hunt.DedupHash("https://upwork.com/fl/list/py"),
		Title:     "Python API",
		URL:       "https://upwork.com/fl/list/py",
		Platform:  "freelancer",
		Source:    "freelancer",
		Skills:    []string{"python"},
	})
	require.NoError(t, err)

	goResults, err := s.ListFreelance(ctx, hunt.FreelanceFilter{Skills: []string{"go"}, Limit: 10})
	require.NoError(t, err)
	assert.Len(t, goResults, 1)
	assert.Equal(t, "Go API", goResults[0].Title)
}

// --- ListSecurity ---

// TestStore_ListSecurity_Basic verifies ListSecurity returns rows with optional platform filter.
func TestStore_ListSecurity_Basic(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))
	truncateSecurity(t, pool)

	_, _, err := s.UpsertSecurity(ctx, hunt.Security{
		DedupHash: hunt.DedupHash("https://hackerone.com/programs/listtest"),
		Name:      "List Target",
		URL:       "https://hackerone.com/programs/listtest",
		Platform:  "hackerone",
		MaxBounty: 10000,
	})
	require.NoError(t, err)

	results, err := s.ListSecurity(ctx, hunt.SecurityFilter{Limit: 10})
	require.NoError(t, err)
	assert.Len(t, results, 1)

	filtered, err := s.ListSecurity(ctx, hunt.SecurityFilter{Platform: "bugcrowd", Limit: 10})
	require.NoError(t, err)
	assert.Len(t, filtered, 0, "platform filter should exclude hackerone record")
}

// --- ListAuditContests ---

// TestStore_ListAuditContests_Basic verifies ListAuditContests returns rows.
func TestStore_ListAuditContests_Basic(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))
	truncateAuditContests(t, pool)

	_, _, err := s.UpsertAuditContest(ctx, hunt.AuditContest{
		DedupHash: hunt.DedupHash("https://code4rena.com/contests/list-test"),
		Title:     "List Contest",
		URL:       "https://code4rena.com/contests/list-test",
		Platform:  "code4rena",
		TotalPool: 75000,
		Currency:  "USDC",
	})
	require.NoError(t, err)

	results, err := s.ListAuditContests(ctx, hunt.AuditContestFilter{Limit: 10})
	require.NoError(t, err)
	assert.Len(t, results, 1)
}

// --- ListRatings ---

// TestStore_ListRatings_Basic verifies ListRatings returns user ratings.
func TestStore_ListRatings_Basic(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))
	truncateRatings(t, pool)

	err := s.Rate(ctx, hunt.KindBounty, 42, "krolik", hunt.StageInteresting, "")
	require.NoError(t, err)
	err = s.Rate(ctx, hunt.KindJob, 7, "krolik", hunt.StageSaved, "good fit")
	require.NoError(t, err)

	results, err := s.ListRatings(ctx, hunt.RatingFilter{User: "krolik", Limit: 10})
	require.NoError(t, err)
	assert.Len(t, results, 2, "should return both ratings for user krolik")
}

// --- OutcomeError separated from OutcomeSkipped ---

// TestOutcomeError_String verifies OutcomeError has a distinct label from OutcomeSkipped.
func TestOutcomeError_String(t *testing.T) {
	assert.Equal(t, "error", hunt.OutcomeError.String())
	assert.Equal(t, "skipped", hunt.OutcomeSkipped.String())
	assert.NotEqual(t, hunt.OutcomeError.String(), hunt.OutcomeSkipped.String(),
		"OutcomeError and OutcomeSkipped must have distinct metric labels")
}

// --- Raw JSONB round-trip ---

// TestStore_UpsertBounty_RawRoundTrip verifies Raw JSONB is stored and retrievable.
func TestStore_UpsertBounty_RawRoundTrip(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))
	truncateBounties(t, pool)

	rawData := json.RawMessage(`{"title":"Fix bug","source":"algora","amount":"$500"}`)
	b := hunt.Bounty{
		DedupHash: hunt.DedupHash("https://github.com/raw/round/trip"),
		Title:     "Raw Round-trip",
		URL:       "https://github.com/raw/round/trip",
		Source:    "algora",
		Raw:       rawData,
	}

	id, _, err := s.UpsertBounty(ctx, b)
	require.NoError(t, err)

	got, err := s.GetBountyWithRaw(ctx, id)
	require.NoError(t, err)
	assert.NotEmpty(t, got.Raw, "Raw field must be persisted and retrievable")

	// Verify the JSON round-trips: both must have the same 'source' key.
	var original, retrieved map[string]any
	require.NoError(t, json.Unmarshal(rawData, &original))
	require.NoError(t, json.Unmarshal(got.Raw, &retrieved))
	assert.Equal(t, original["source"], retrieved["source"], "Raw JSON content must round-trip through Postgres")
}
