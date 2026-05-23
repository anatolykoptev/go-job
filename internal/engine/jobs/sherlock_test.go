package jobs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// TestMain initializes engine.Cfg.HTTPClient so unit tests can exercise fetchSherlockRepos
// without calling engine.Init (which requires a full config including proxy pool etc.).
func TestMain(m *testing.M) {
	engine.Cfg.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	engine.Cfg.FetchTimeout = 10 * time.Second
	os.Exit(m.Run())
}

// TestPrettifySherlockName verifies repo name → display name conversion.
func TestPrettifySherlockName(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"2024-12-foo-protocol-judging", "Foo Protocol (2024-12)"},
		{"2025-03-bar", "Bar (2025-03)"},
		{"random-non-conforming", "random-non-conforming"},
	}
	for _, c := range cases {
		got := prettifySherlockName(c.raw)
		if got != c.want {
			t.Errorf("prettifySherlockName(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}

// buildSherlockFixture returns a JSON byte slice with 2 active + 1 archived + 1 fork repo.
func buildSherlockFixture() []byte {
	repos := []sherlockRepo{
		{Name: "2024-12-foo-protocol-judging", HTMLURL: "https://github.com/sherlock-audit/2024-12-foo-protocol-judging", Archived: false, Fork: false},
		{Name: "2025-01-bar-protocol-judging", HTMLURL: "https://github.com/sherlock-audit/2025-01-bar-protocol-judging", Archived: false, Fork: false},
		{Name: "2024-11-archived-judging", HTMLURL: "https://github.com/sherlock-audit/2024-11-archived-judging", Archived: true, Fork: false},
		{Name: "2024-10-forked", HTMLURL: "https://github.com/sherlock-audit/2024-10-forked", Archived: false, Fork: true},
	}
	b, _ := json.Marshal(repos)
	return b
}

// TestSearchSherlock_HTTPMock verifies filtering and field mapping via a mock HTTP server.
func TestSearchSherlock_HTTPMock(t *testing.T) {
	fixture := buildSherlockFixture()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture)
	}))
	defer srv.Close()

	// Override the base URL so SearchSherlock hits the mock.
	orig := sherlockBaseURL
	sherlockBaseURL = srv.URL
	defer func() { sherlockBaseURL = orig }()

	// Use a unique cache key per test run to avoid cross-test pollution.
	origKey := sherlockCacheKey
	sherlockCacheKey = "sherlock_audits_test_mock"
	defer func() { sherlockCacheKey = origKey }()

	ctx := context.Background()
	programs, err := SearchSherlock(ctx, 10)
	if err != nil {
		t.Fatalf("SearchSherlock: unexpected error: %v", err)
	}

	// 3 programs expected: 2 active + 1 archived; fork is always excluded.
	// Phase 3 change: archived repos now pass through so the mapper can set StatusArchived
	// instead of silently dropping them. Fork repos are still excluded (always noise).
	if len(programs) != 3 {
		t.Fatalf("want 3 programs (2 active + 1 archived), got %d", len(programs))
	}

	var archivedCount int
	for i, p := range programs {
		if p.Platform != "sherlock" {
			t.Errorf("[%d] Platform = %q, want %q", i, p.Platform, "sherlock")
		}
		if p.Type != "audit_contest" {
			t.Errorf("[%d] Type = %q, want %q", i, p.Type, "audit_contest")
		}
		if !p.Managed {
			t.Errorf("[%d] Managed = false, want true", i)
		}
		if p.URL == "" {
			t.Errorf("[%d] URL is empty", i)
		}
		if p.Name == "" {
			t.Errorf("[%d] Name is empty", i)
		}
		if p.Archived {
			archivedCount++
		}
	}
	if archivedCount != 1 {
		t.Errorf("want 1 archived program in results, got %d", archivedCount)
	}
}

// TestSearchSherlock_Cache verifies that a second call returns cached data even when
// the mock server is closed.
func TestSearchSherlock_Cache(t *testing.T) {
	// InitCache with in-memory L1 only (no Redis) so CacheStoreJSON is functional.
	engine.InitCache("", 15*time.Minute, 200, 5*time.Minute)

	fixture := buildSherlockFixture()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture)
	}))

	orig := sherlockBaseURL
	sherlockBaseURL = srv.URL
	defer func() { sherlockBaseURL = orig }()

	origKey := sherlockCacheKey
	sherlockCacheKey = "sherlock_audits_test_cache"
	defer func() { sherlockCacheKey = origKey }()

	ctx := context.Background()

	// First call — populates cache.
	first, err := SearchSherlock(ctx, 10)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}

	// Close the server — any subsequent HTTP call would fail.
	srv.Close()
	sherlockBaseURL = "http://127.0.0.1:0" // unreachable

	// Second call — must succeed from cache.
	second, err := SearchSherlock(ctx, 10)
	if err != nil {
		t.Fatalf("second call (should be cached): %v", err)
	}

	if len(first) != len(second) {
		t.Errorf("cache mismatch: first=%d second=%d", len(first), len(second))
	}

	// Verify names match.
	for i := range first {
		if first[i].Name != second[i].Name {
			t.Errorf("[%d] name mismatch: %q vs %q", i, first[i].Name, second[i].Name)
		}
	}

	// Spot-check: fork names must not appear; archived repos ARE now included (Phase 3).
	for _, p := range second {
		if strings.Contains(strings.ToLower(p.Name), "forked") {
			t.Errorf("forked repo leaked into results: %q", p.Name)
		}
	}
}

// TestSearchSherlock_LimitEnforced verifies that limit is respected.
func TestSearchSherlock_LimitEnforced(t *testing.T) {
	// Build a fixture with 4 valid repos.
	repos := make([]sherlockRepo, 4)
	for i := range repos {
		repos[i] = sherlockRepo{
			Name:    "2025-01-proto" + string(rune('a'+i)) + "-judging",
			HTMLURL: "https://github.com/sherlock-audit/repo",
		}
	}
	b, _ := json.Marshal(repos)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	orig := sherlockBaseURL
	sherlockBaseURL = srv.URL
	defer func() { sherlockBaseURL = orig }()

	origKey := sherlockCacheKey
	sherlockCacheKey = "sherlock_audits_test_limit"
	defer func() { sherlockCacheKey = origKey }()

	programs, err := SearchSherlock(context.Background(), 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(programs) != 2 {
		t.Errorf("want 2 programs (limit=2), got %d", len(programs))
	}
}

// Compile-time check: engine.SecurityProgram.Managed field used in tests.
var _ = engine.SecurityProgram{}
