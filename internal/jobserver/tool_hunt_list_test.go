package jobserver

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestDescriptionTruncation verifies truncateEntryDescriptions behaviour:
//   - descriptions longer than huntListDescSnippetLen runes are truncated to
//     exactly huntListDescSnippetLen runes + "…" and flagged with
//     description_truncated=true
//   - short descriptions are left intact with no description_truncated key
func TestDescriptionTruncation(t *testing.T) {
	t.Run("long description is truncated", func(t *testing.T) {
		longDesc := strings.Repeat("a", huntListDescSnippetLen+50)
		entry := map[string]any{
			"title":       "Test Job",
			"description": longDesc,
		}
		entries := []map[string]any{entry}

		truncateEntryDescriptions(entries)

		got, ok := entries[0]["description"].(string)
		if !ok {
			t.Fatal("description should still be a string after truncation")
		}
		runes := []rune(got)
		// Must end with the ellipsis character (1 rune).
		if !strings.HasSuffix(got, "…") {
			t.Errorf("truncated description should end with '…', got: %q", got)
		}
		// Total rune length must be huntListDescSnippetLen + 1 (the ellipsis rune).
		wantLen := huntListDescSnippetLen + 1
		if len(runes) != wantLen {
			t.Errorf("truncated description rune length = %d, want %d", len(runes), wantLen)
		}
		// description_truncated must be true.
		trunc, exists := entries[0]["description_truncated"]
		if !exists {
			t.Error("description_truncated key should be present for truncated entries")
		}
		if trunc != true {
			t.Errorf("description_truncated should be true, got %v", trunc)
		}
	})

	t.Run("short description is left intact", func(t *testing.T) {
		shortDesc := strings.Repeat("b", 100)
		entry := map[string]any{
			"title":       "Short Job",
			"description": shortDesc,
		}
		entries := []map[string]any{entry}

		truncateEntryDescriptions(entries)

		got, ok := entries[0]["description"].(string)
		if !ok {
			t.Fatal("description should still be a string")
		}
		if got != shortDesc {
			t.Errorf("short description should be unchanged; got %q", got)
		}
		// description_truncated must NOT be present.
		if _, exists := entries[0]["description_truncated"]; exists {
			t.Error("description_truncated should not be set for short descriptions")
		}
	})

	t.Run("exactly huntListDescSnippetLen runes is not truncated", func(t *testing.T) {
		exactDesc := strings.Repeat("c", huntListDescSnippetLen)
		entry := map[string]any{"description": exactDesc}
		entries := []map[string]any{entry}

		truncateEntryDescriptions(entries)

		got := entries[0]["description"].(string)
		if got != exactDesc {
			t.Errorf("exactly-limit description should be unchanged")
		}
		if _, exists := entries[0]["description_truncated"]; exists {
			t.Error("description_truncated should not be set for exactly-limit descriptions")
		}
	})

	t.Run("entry without description key is unaffected", func(t *testing.T) {
		entry := map[string]any{"title": "No Desc"}
		entries := []map[string]any{entry}

		truncateEntryDescriptions(entries)

		if _, ok := entries[0]["description"]; ok {
			t.Error("description key should not be added when not present")
		}
		if _, ok := entries[0]["description_truncated"]; ok {
			t.Error("description_truncated should not be added when no description present")
		}
	})
}

// TestRuneSafety verifies that truncation cuts on a rune boundary (not a byte
// boundary), preserving valid UTF-8 for multibyte characters.
func TestRuneSafety(t *testing.T) {
	t.Run("multibyte runes at boundary are not corrupted", func(t *testing.T) {
		// Build a description where the boundary falls in the middle of a
		// multi-byte sequence if we naively slice by byte index.
		// Each "日" character is 3 bytes in UTF-8.
		// Place exactly huntListDescSnippetLen of them so the next char is at the boundary.
		japanese := strings.Repeat("日", huntListDescSnippetLen) + "本語テスト"
		entry := map[string]any{"description": japanese}
		entries := []map[string]any{entry}

		truncateEntryDescriptions(entries)

		got, ok := entries[0]["description"].(string)
		if !ok {
			t.Fatal("description should remain a string")
		}
		// Result must be valid UTF-8.
		if !utf8.ValidString(got) {
			t.Error("truncated description is not valid UTF-8")
		}
		// Must end with ellipsis.
		if !strings.HasSuffix(got, "…") {
			t.Errorf("should end with '…', got: %q", got)
		}
		// Rune count must be huntListDescSnippetLen + 1 (the ellipsis).
		runeCount := len([]rune(got))
		wantRunes := huntListDescSnippetLen + 1
		if runeCount != wantRunes {
			t.Errorf("rune count = %d, want %d", runeCount, wantRunes)
		}
		// description_truncated must be true.
		if entries[0]["description_truncated"] != true {
			t.Error("description_truncated should be true after rune-boundary truncation")
		}
	})

	t.Run("multibyte description under limit is untouched", func(t *testing.T) {
		// 10 Japanese characters — well under the 300-rune limit.
		short := strings.Repeat("日", 10)
		entry := map[string]any{"description": short}
		entries := []map[string]any{entry}

		truncateEntryDescriptions(entries)

		got := entries[0]["description"].(string)
		if got != short {
			t.Errorf("short multibyte description should be unchanged, got %q", got)
		}
		if !utf8.ValidString(got) {
			t.Error("short multibyte description should remain valid UTF-8")
		}
		if _, exists := entries[0]["description_truncated"]; exists {
			t.Error("description_truncated should not be set for short descriptions")
		}
	})
}
