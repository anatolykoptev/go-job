package hunt_test

import (
	"testing"

	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/stretchr/testify/assert"
)

// TestCanonicalURL_LowercaseHost verifies scheme+host are lowercased.
func TestCanonicalURL_LowercaseHost(t *testing.T) {
	got := hunt.CanonicalURL("HTTPS://X.COM/a")
	assert.Equal(t, "https://x.com/a", got)
}

// TestCanonicalURL_StripDefaultPort verifies http:80 and https:443 are stripped.
func TestCanonicalURL_StripDefaultPort(t *testing.T) {
	assert.Equal(t, "http://x.com/a", hunt.CanonicalURL("http://x.com:80/a"))
	assert.Equal(t, "https://x.com/a", hunt.CanonicalURL("https://x.com:443/a"))
}

// TestCanonicalURL_NonDefaultPortPreserved verifies non-default ports are kept.
func TestCanonicalURL_NonDefaultPortPreserved(t *testing.T) {
	assert.Equal(t, "https://x.com:8080/a", hunt.CanonicalURL("https://x.com:8080/a"))
}

// TestCanonicalURL_TrimTrailingSlash verifies trailing slash is removed from path.
func TestCanonicalURL_TrimTrailingSlash(t *testing.T) {
	assert.Equal(t, "https://x.com/a", hunt.CanonicalURL("https://x.com/a/"))
}

// TestCanonicalURL_RootPathPreserved verifies bare root "/" is not mangled.
func TestCanonicalURL_RootPathPreserved(t *testing.T) {
	got := hunt.CanonicalURL("https://x.com/")
	// root slash stays as empty path or "/" — both acceptable; host must be lowercase
	assert.Equal(t, "https://x.com", got)
}

// TestCanonicalURL_StripUTM verifies utm_* params are dropped, rest kept.
func TestCanonicalURL_StripUTM(t *testing.T) {
	got := hunt.CanonicalURL("https://x.com/page?utm_source=email&id=42")
	assert.Equal(t, "https://x.com/page?id=42", got)
}

// TestCanonicalURL_StripUTMOnly verifies all-UTM query becomes empty (no trailing ?).
func TestCanonicalURL_StripUTMOnly(t *testing.T) {
	got := hunt.CanonicalURL("https://x.com/page?utm_source=email")
	assert.Equal(t, "https://x.com/page", got)
}

// TestCanonicalURL_SortQueryParams verifies query params are sorted alphabetically.
func TestCanonicalURL_SortQueryParams(t *testing.T) {
	got := hunt.CanonicalURL("https://x.com/page?b=2&a=1")
	assert.Equal(t, "https://x.com/page?a=1&b=2", got)
}

// TestCanonicalURL_StripFragment verifies #fragment is dropped.
func TestCanonicalURL_StripFragment(t *testing.T) {
	got := hunt.CanonicalURL("https://x.com/a#section")
	assert.Equal(t, "https://x.com/a", got)
}

// TestCanonicalURL_StripTrk verifies trk tracking param is dropped.
func TestCanonicalURL_StripTrk(t *testing.T) {
	got := hunt.CanonicalURL("https://linkedin.com/jobs/view/123?trk=abc&id=1")
	assert.Equal(t, "https://linkedin.com/jobs/view/123?id=1", got)
}

// TestCanonicalURL_StripFbclid verifies fbclid is dropped.
func TestCanonicalURL_StripFbclid(t *testing.T) {
	got := hunt.CanonicalURL("https://x.com/page?fbclid=XYZ&q=go")
	assert.Equal(t, "https://x.com/page?q=go", got)
}

// TestCanonicalURL_StripGclid verifies gclid is dropped.
func TestCanonicalURL_StripGclid(t *testing.T) {
	got := hunt.CanonicalURL("https://x.com/page?gclid=CjwA&q=go")
	assert.Equal(t, "https://x.com/page?q=go", got)
}

// TestCanonicalURL_MalformedFallback verifies non-parseable URLs pass through unchanged.
func TestCanonicalURL_MalformedFallback(t *testing.T) {
	got := hunt.CanonicalURL("not a url")
	assert.Equal(t, "not a url", got)
}

// TestCanonicalURL_LinkedInDedup verifies two LinkedIn URLs with different tracking
// params produce identical canonical form.
func TestCanonicalURL_LinkedInDedup(t *testing.T) {
	a := hunt.CanonicalURL("https://linkedin.com/jobs/view/123?utm_source=email&trk=abc")
	b := hunt.CanonicalURL("https://linkedin.com/jobs/view/123")
	assert.Equal(t, a, b, "same job URL with/without tracking must canonicalize identically")
}

// --- CanonicalURLForSource ---

// TestCanonicalURLForSource_UnknownSource falls through to CanonicalURL.
func TestCanonicalURLForSource_UnknownSource(t *testing.T) {
	raw := "https://linkedin.com/jobs/view/123?utm_source=email"
	got := hunt.CanonicalURLForSource(raw, "linkedin")
	want := hunt.CanonicalURL(raw)
	assert.Equal(t, want, got)
}

// TestCanonicalURLForSource_NonGitHub_Algora passes an Algora URL without a
// recognisable GitHub issue path; should fall back to CanonicalURL.
func TestCanonicalURLForSource_NonGitHub_Algora(t *testing.T) {
	raw := "https://algora.io/bounties"
	got := hunt.CanonicalURLForSource(raw, "algora")
	want := hunt.CanonicalURL(raw)
	assert.Equal(t, want, got)
}

// TestCanonicalURLForSource_Algora verifies that an Algora wrapper URL carrying
// a GitHub issue URL in the "id" query param resolves to the canonical github URL.
func TestCanonicalURLForSource_Algora(t *testing.T) {
	raw := "https://algora.io/issues?id=github.com/foo/bar/issues/42"
	got := hunt.CanonicalURLForSource(raw, "algora")
	assert.Equal(t, "https://github.com/foo/bar/issues/42", got)
}

// TestCanonicalURLForSource_Opire verifies opire wrapper with github_url param.
func TestCanonicalURLForSource_Opire(t *testing.T) {
	raw := "https://opire.dev/bounty/abc-xyz?github_url=github.com%2Ffoo%2Fbar%2Fissues%2F99"
	got := hunt.CanonicalURLForSource(raw, "opire")
	assert.Equal(t, "https://github.com/foo/bar/issues/99", got)
}

// TestCanonicalURLForSource_BountyHub verifies bountyhub wrapper with github_url param.
func TestCanonicalURLForSource_BountyHub(t *testing.T) {
	raw := "https://bountyhub.dev/issues/123?github_url=github.com%2Ffoo%2Fbar%2Fissues%2F7"
	got := hunt.CanonicalURLForSource(raw, "bountyhub")
	assert.Equal(t, "https://github.com/foo/bar/issues/7", got)
}

// --- DedupHashForSource cross-source invariance ---

// TestDedupHashForSource_SameGitHubURL_FromAlgoraAndOpire verifies that Algora and
// Opire wrapper URLs pointing to the same GitHub issue produce the same dedup hash.
func TestDedupHashForSource_SameGitHubURL_FromAlgoraAndOpire(t *testing.T) {
	algoraURL := "https://algora.io/issues?id=github.com/foo/bar/issues/42"
	opireURL := "https://opire.dev/bounty/abc?github_url=github.com%2Ffoo%2Fbar%2Fissues%2F42"

	ha := hunt.DedupHashForSource(algoraURL, "algora")
	ho := hunt.DedupHashForSource(opireURL, "opire")
	assert.Equal(t, ha, ho, "Algora and Opire wrapper URLs for the same GitHub issue must produce identical dedup hash")
}

// TestDedupHashForSource_PlainGitHubURLIsIdempotent verifies that a plain GitHub URL
// is unaffected by source-aware canonicalization.
func TestDedupHashForSource_PlainGitHubURLIsIdempotent(t *testing.T) {
	ghURL := "https://github.com/foo/bar/issues/42"
	h1 := hunt.DedupHash(ghURL)
	h2 := hunt.DedupHashForSource(ghURL, "algora")
	assert.Equal(t, h1, h2, "plain GitHub URL must hash identically regardless of source")
}
