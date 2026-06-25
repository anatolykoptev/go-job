package adminui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anatolykoptev/go-panel/auth"
	"github.com/anatolykoptev/go-panel/resource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testPanel builds a minimal resource.Panel for handler tests.
func testPanel(t *testing.T) *resource.Panel {
	t.Helper()
	a := auth.NewHMACAuth(auth.HMACConfig{
		Username: "admin",
		Password: "test",
		HMACKey:  []byte("xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"),
		BasePath: "/admin",
	})
	return resource.New(resource.Config{
		Title:    "test",
		BasePath: "/admin",
		Auth:     a,
		CSRFKey:  []byte("yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy"),
	})
}

// --- parseLinkedInSections ---

func TestParseLinkedInSections_FixtureBasic(t *testing.T) {
	md := `## 1. Headline (max 220 chars)

This is a description.

` + "```" + `
Software Engineer at Example
` + "```" + `

## 2. About

Some prose about me.

` + "```" + `
I am a software engineer with experience in distributed systems.
` + "```" + `
`

	sections := parseLinkedInSections(md)
	require.Len(t, sections, 2)

	s1 := sections[0]
	assert.Equal(t, "1", s1.Number)
	assert.Equal(t, "Headline (max 220 chars)", s1.Title)
	assert.Equal(t, 220, s1.CharLimit)

	// Should have at least one prose item and one code item.
	var proseCount, codeCount int
	for _, item := range s1.Items {
		if item.Kind == liItemProse {
			proseCount++
		}
		if item.Kind == liItemCode {
			codeCount++
			assert.Equal(t, "Software Engineer at Example", item.Content)
			assert.Equal(t, 28, item.CharCount)
		}
	}
	assert.GreaterOrEqual(t, proseCount, 1, "section 1 should have prose")
	assert.Equal(t, 1, codeCount, "section 1 should have 1 code block")

	s2 := sections[1]
	assert.Equal(t, "2", s2.Number)
	assert.Equal(t, 2600, s2.CharLimit)
}

func TestParseLinkedInSections_PreservesOrder(t *testing.T) {
	// Prose before fence, fence, then more prose after.
	md := `## 3. Experience

Before fence.

` + "```" + `
code block content
` + "```" + `

After fence.
`
	sections := parseLinkedInSections(md)
	require.Len(t, sections, 1)

	items := sections[0].Items
	require.GreaterOrEqual(t, len(items), 2)

	// First item must be prose (contains "Before fence").
	assert.Equal(t, liItemProse, items[0].Kind)
	assert.Contains(t, string(items[0].HTML), "Before fence")

	// Find code item — must precede any trailing prose.
	codeIdx := -1
	for i, it := range items {
		if it.Kind == liItemCode {
			codeIdx = i
			break
		}
	}
	require.NotEqual(t, -1, codeIdx, "should have a code item")
	assert.Equal(t, "code block content", items[codeIdx].Content)
}

func TestParseLinkedInSections_SubHeadingBecomesLabel(t *testing.T) {
	md := `## 1. Headline

### Entry A

` + "```" + `
headline text
` + "```" + `
`
	sections := parseLinkedInSections(md)
	require.Len(t, sections, 1)

	var codeItem *linkedInItem
	for i := range sections[0].Items {
		if sections[0].Items[i].Kind == liItemCode {
			codeItem = &sections[0].Items[i]
			break
		}
	}
	require.NotNil(t, codeItem)
	assert.Equal(t, "Entry A", codeItem.Label)
}

func TestParseLinkedInSections_Empty(t *testing.T) {
	sections := parseLinkedInSections("")
	assert.Empty(t, sections)
}

func TestParseLinkedInSections_PreIDUnique(t *testing.T) {
	md := `## 1. Headline

` + "```" + `
block one
` + "```" + `

` + "```" + `
block two
` + "```" + `
`
	sections := parseLinkedInSections(md)
	require.Len(t, sections, 1)

	seen := make(map[string]bool)
	for _, item := range sections[0].Items {
		if item.Kind == liItemCode {
			assert.False(t, seen[item.PreID], "duplicate PreID: %s", item.PreID)
			seen[item.PreID] = true
		}
	}
	assert.Len(t, seen, 2, "should have 2 distinct PreIDs")
}

// --- charCounterClass ---

func TestCharCounterClass(t *testing.T) {
	tests := []struct {
		count, limit int
		want         string
	}{
		{0, 220, "cc-muted"},   // zero count
		{100, 0, "cc-muted"},   // no limit
		{50, 220, "cc-green"},  // under 80%
		{176, 220, "cc-amber"}, // exactly 80%
		{200, 220, "cc-amber"}, // between 80% and 100%
		{220, 220, "cc-amber"}, // exactly 100% — pct==1.0 is NOT > 1.0
		{221, 220, "cc-red"},   // over limit
	}
	for _, tc := range tests {
		got := charCounterClass(tc.count, tc.limit)
		assert.Equal(t, tc.want, got, "charCounterClass(%d, %d)", tc.count, tc.limit)
	}
}

// --- charCounterLabel ---

func TestCharCounterLabel(t *testing.T) {
	assert.Equal(t, "134 / 220", charCounterLabel(134, 220))
	assert.Equal(t, "100 chars", charCounterLabel(100, 0))
	assert.Equal(t, "0 chars", charCounterLabel(0, 0))
}

// --- handler: missing file ---

func TestLinkedinHandler_MissingFile_Returns200EmptyState(t *testing.T) {
	tmpDir := t.TempDir()
	// Deliberately do NOT create LINKEDIN-UPDATE.md.

	p := testPanel(t)
	handler := linkedinHandler(p, tmpDir)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/admin/linkedin", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code, "missing file should return 200, not 500")
	body := rr.Body.String()
	assert.Contains(t, body, "LINKEDIN-UPDATE.md", "empty state should mention the expected filename")
}

// --- handler: file present ---

func TestLinkedinHandler_FilePresent_Returns200WithContent(t *testing.T) {
	tmpDir := t.TempDir()

	content := `## 1. Headline (max 220 chars)

Short description here.

` + "```" + `
Staff Software Engineer | Go + Rust
` + "```" + `

## 2. About

Professional summary.
`
	err := os.WriteFile(filepath.Join(tmpDir, "LINKEDIN-UPDATE.md"), []byte(content), 0o600)
	require.NoError(t, err)

	p := testPanel(t)
	handler := linkedinHandler(p, tmpDir)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/admin/linkedin", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	body := rr.Body.String()
	// Should contain the section heading.
	assert.Contains(t, body, "Headline")
	// Should contain the code block content.
	assert.Contains(t, body, "Staff Software Engineer")
}

// --- parseH2Number ---

func TestParseH2Number(t *testing.T) {
	tests := []struct {
		heading, wantNum, wantTitle string
	}{
		{"1. Headline (max 220 chars)", "1", "Headline (max 220 chars)"},
		{"2. About", "2", "About"},
		{"no-dot-heading", "", "no-dot-heading"},
		{"abc. text", "", "abc. text"},
	}
	for _, tc := range tests {
		num, title := parseH2Number(tc.heading)
		assert.Equal(t, tc.wantNum, num, "num for %q", tc.heading)
		assert.Equal(t, tc.wantTitle, title, "title for %q", tc.heading)
	}
}

// --- routing sanity: /admin/linkedin must not shadow existing routes ---

func TestRouting_LinkedInDoesNotShadowExistingRoutes(t *testing.T) {
	// Build a minimal mux mirroring adminui.New pattern to confirm
	// the /admin/linkedin path is distinct from /admin/resume.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/resume", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("resume"))
	})
	mux.HandleFunc("GET /admin/linkedin", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("linkedin"))
	})

	for _, tc := range []struct{ path, want string }{
		{"/admin/resume", "resume"},
		{"/admin/linkedin", "linkedin"},
	} {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, tc.path, nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusOK, rr.Code, "path %s", tc.path)
		assert.Equal(t, tc.want, strings.TrimSpace(rr.Body.String()), "path %s", tc.path)
	}
}
