package webtext

// Truncate returns the first n bytes of s, ensuring a valid UTF-8 boundary.
// Returns s unchanged if n <= 0 or s is already short enough.
func Truncate(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	// Back up from n to find a valid UTF-8 start byte.
	for n > 0 && n < len(s) {
		if s[n]&0xC0 != 0x80 { // not a continuation byte
			break
		}
		n--
	}
	return s[:n]
}

// DefaultCharsPerToken is the average bytes per LLM token for English text.
// Multilingual text (Cyrillic, CJK) may need ~2.5.
const DefaultCharsPerToken = 3.5
