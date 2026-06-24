package jobs

import (
	"testing"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLeverCreatedAtToISO covers the epoch-millisecond → RFC3339 conversion that
// the lever fetcher uses (Lever delivers createdAt in epoch ms, not seconds).
func TestLeverCreatedAtToISO(t *testing.T) {
	cases := []struct {
		name string
		ms   int64
		want string
	}{
		{"valid epoch ms", 1705314600000, "2024-01-15T10:30:00Z"},
		{"zero is absent", 0, ""},
		{"negative is absent", -1, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, leverCreatedAtToISO(tc.ms))
		})
	}
}

// TestSetPostedAtMeta verifies the metadata key is set (with lazy map allocation)
// for a non-empty date and is a no-op for an empty date.
func TestSetPostedAtMeta(t *testing.T) {
	t.Run("sets key and lazily allocates map", func(t *testing.T) {
		r := engine.SearxngResult{Title: "X"}
		require.Nil(t, r.Metadata, "precondition: fetchers build results with no Metadata")
		setPostedAtMeta(&r, "2024-01-15T10:30:00Z")
		require.NotNil(t, r.Metadata)
		assert.Equal(t, "2024-01-15T10:30:00Z", r.Metadata[metaKeyPostedAt])
	})

	t.Run("empty date is a no-op", func(t *testing.T) {
		r := engine.SearxngResult{Title: "X"}
		setPostedAtMeta(&r, "")
		assert.Nil(t, r.Metadata, "empty date must not allocate the map or add the key")
	})

	t.Run("preserves existing metadata", func(t *testing.T) {
		r := engine.SearxngResult{Metadata: map[string]string{"other": "v"}}
		setPostedAtMeta(&r, "2024-01-15T10:30:00Z")
		assert.Equal(t, "v", r.Metadata["other"])
		assert.Equal(t, "2024-01-15T10:30:00Z", r.Metadata[metaKeyPostedAt])
	})
}

// TestSearxngResultToHuntJob_PostedAt is the end-to-end guard for the worker
// ingest path (the one that does NOT route through the LLM). It mirrors what each
// platform fetcher threads into Metadata[metaKeyPostedAt]:
//   - greenhouse: job.UpdatedAt (RFC3339 ISO) verbatim
//   - ashby:      j.PublishedAt (RFC3339 ISO) verbatim
//   - lever:      leverCreatedAtToISO(p.CreatedAt) (epoch-ms → RFC3339)
//
// and asserts a parseable date lands in PostedAt while an absent/garbage value
// keeps it nil (→ NULL posted_at, the pre-fix behaviour for that single row).
func TestSearxngResultToHuntJob_PostedAt(t *testing.T) {
	cases := []struct {
		name     string
		platform string
		meta     map[string]string
		wantSet  bool
	}{
		{
			name:     "greenhouse ISO date populates PostedAt",
			platform: engine.DiscoveryPlatformGreenhouse,
			meta:     map[string]string{metaKeyPostedAt: "2024-01-15T10:30:00-05:00"},
			wantSet:  true,
		},
		{
			name:     "ashby ISO date populates PostedAt",
			platform: engine.DiscoveryPlatformAshby,
			meta:     map[string]string{metaKeyPostedAt: "2024-01-15T10:30:00Z"},
			wantSet:  true,
		},
		{
			name:     "lever epoch-ms-derived ISO populates PostedAt",
			platform: engine.DiscoveryPlatformLever,
			meta:     map[string]string{metaKeyPostedAt: leverCreatedAtToISO(1705314600000)},
			wantSet:  true,
		},
		{
			name:     "date-only ISO populates PostedAt",
			platform: engine.DiscoveryPlatformGreenhouse,
			meta:     map[string]string{metaKeyPostedAt: "2024-01-15"},
			wantSet:  true,
		},
		{
			name:     "absent metadata yields nil PostedAt",
			platform: engine.DiscoveryPlatformGreenhouse,
			meta:     nil,
			wantSet:  false,
		},
		{
			name:     "empty metadata value yields nil PostedAt",
			platform: engine.DiscoveryPlatformLever,
			meta:     map[string]string{metaKeyPostedAt: ""},
			wantSet:  false,
		},
		{
			name:     "garbage metadata value yields nil PostedAt",
			platform: engine.DiscoveryPlatformAshby,
			meta:     map[string]string{metaKeyPostedAt: "not-a-date"},
			wantSet:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := engine.SearxngResult{
				Title:    "Engineer",
				URL:      "https://example.com/jobs/1",
				Content:  "snippet",
				Metadata: tc.meta,
			}
			j := SearxngResultToHuntJob(r, tc.platform)
			assert.Equal(t, tc.platform, j.Source, "source must be the platform")
			if tc.wantSet {
				require.NotNil(t, j.PostedAt, "expected a parsed PostedAt")
				assert.False(t, j.PostedAt.IsZero(), "parsed time must not be the zero value")
			} else {
				assert.Nil(t, j.PostedAt, "expected nil PostedAt (→ NULL posted_at)")
			}
		})
	}
}
