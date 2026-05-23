package hunt_test

import (
	"testing"

	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/stretchr/testify/assert"
)

func TestDedupHash_Deterministic(t *testing.T) {
	url := "https://github.com/org/repo/issues/42"
	h1 := hunt.DedupHash(url)
	h2 := hunt.DedupHash(url)
	assert.Equal(t, h1, h2, "same URL must produce same hash")
	assert.Len(t, h1, 32, "hash must be 32 hex chars (16 bytes)")
}

// TestDedupHash_NormalizesCase verifies that scheme and host are case-normalised.
// Phase 2: only scheme+host are lowercased; URL path is kept as-is (paths are
// case-sensitive on GitHub and most hosts). Two URLs that differ only in
// scheme/host case must hash identically.
func TestDedupHash_NormalizesCase(t *testing.T) {
	lower := hunt.DedupHash("https://github.com/org/repo/issues/42")
	upper := hunt.DedupHash("HTTPS://GITHUB.COM/org/repo/issues/42")
	assert.Equal(t, lower, upper, "scheme+host case normalisation must produce equal hashes")
}

func TestDedupHash_TrimsWhitespace(t *testing.T) {
	clean := hunt.DedupHash("https://github.com/org/repo/issues/42")
	padded := hunt.DedupHash("  https://github.com/org/repo/issues/42  ")
	assert.Equal(t, clean, padded, "surrounding whitespace must be trimmed before hashing")
}

func TestDedupHash_DifferentURLs(t *testing.T) {
	h1 := hunt.DedupHash("https://algora.io/org/repo/issues/1")
	h2 := hunt.DedupHash("https://algora.io/org/repo/issues/2")
	assert.NotEqual(t, h1, h2, "distinct URLs must produce distinct hashes")
}
