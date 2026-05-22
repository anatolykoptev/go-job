package hunt

import (
	"crypto/sha256"
	"encoding/hex"
)

// DedupHash returns the canonical deduplication hash for a URL.
// Same pattern as MemDB textHash: SHA-256 of CanonicalURL output,
// hex-encoded first 16 bytes (32 hex chars).
//
// Phase 1 normalisation: lowercase + trim.
// Phase 2 normalisation: tracking-param strip, port normalisation, fragment strip,
// query sort — delegated to CanonicalURL.
func DedupHash(rawURL string) string {
	canon := CanonicalURL(rawURL)
	sum := sha256.Sum256([]byte(canon))
	return hex.EncodeToString(sum[:16])
}

// DedupHashForSource is the source-aware variant of DedupHash.
// For known aggregator sources (algora, opire, bountyhub) it attempts to resolve
// a wrapped GitHub issue URL from query params before hashing, enabling cross-source
// deduplication of the same underlying GitHub issue.
func DedupHashForSource(rawURL, source string) string {
	canon := CanonicalURLForSource(rawURL, source)
	sum := sha256.Sum256([]byte(canon))
	return hex.EncodeToString(sum[:16])
}
