package cache

import (
	"encoding/hex"
	"hash/fnv"
)

// Key builds a deterministic cache key from parts using FNV-128a.
func Key(parts ...string) string {
	h := fnv.New128a()
	for i, p := range parts {
		if i > 0 {
			h.Write([]byte{0})
		}
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}
