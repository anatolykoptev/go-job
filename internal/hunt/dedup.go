package hunt

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// DedupHash returns the canonical deduplication hash for a URL.
// Same pattern as MemDB textHash: SHA-256 of trimmed-lowercased URL,
// hex-encoded first 16 bytes (32 hex chars).
//
// Phase 1 normalisation: lowercase + trim only.
// Phase 2 will add canonical URL normalisation (algora→github resolve, utm strip).
func DedupHash(canonicalURL string) string {
	norm := strings.ToLower(strings.TrimSpace(canonicalURL))
	sum := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(sum[:16])
}
