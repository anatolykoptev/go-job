package jobs

import (
	"strings"
	"testing"
)

// TestCheckResumeTruncation_NoTruncation verifies that short text is not flagged.
func TestCheckResumeTruncation_NoTruncation(t *testing.T) {
	text := strings.Repeat("a", 100)
	truncated, origLen := checkResumeTruncation(text, 12000)
	if truncated {
		t.Errorf("expected no truncation for 100-rune text, got truncated=true")
	}
	if origLen != 100 {
		t.Errorf("expected origLen=100, got %d", origLen)
	}
}

// TestCheckResumeTruncation_ExactLimit verifies text at the limit is not flagged.
func TestCheckResumeTruncation_ExactLimit(t *testing.T) {
	text := strings.Repeat("b", 12000)
	truncated, origLen := checkResumeTruncation(text, 12000)
	if truncated {
		t.Errorf("expected no truncation at exactly 12000 runes, got truncated=true")
	}
	if origLen != 12000 {
		t.Errorf("expected origLen=12000, got %d", origLen)
	}
}

// TestCheckResumeTruncation_OverLimit verifies that long text is flagged.
func TestCheckResumeTruncation_OverLimit(t *testing.T) {
	text := strings.Repeat("x", 20000)
	truncated, origLen := checkResumeTruncation(text, 12000)
	if !truncated {
		t.Errorf("expected truncation=true for 20000-rune text, got false")
	}
	if origLen != 20000 {
		t.Errorf("expected origLen=20000, got %d", origLen)
	}
}

// TestCheckResumeTruncation_MultibyteSafe verifies rune counting for multibyte (Cyrillic) text.
func TestCheckResumeTruncation_MultibyteSafe(t *testing.T) {
	// Each Cyrillic rune is 2 bytes — 20000 runes = 40000 bytes.
	text := strings.Repeat("я", 20000)
	truncated, origLen := checkResumeTruncation(text, 12000)
	if !truncated {
		t.Errorf("expected truncation=true for 20000-rune Cyrillic text, got false")
	}
	if origLen != 20000 {
		t.Errorf("expected origLen=20000 runes, got %d", origLen)
	}
}
