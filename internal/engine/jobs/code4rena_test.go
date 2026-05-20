package jobs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// sampleCode4renaHTML is a minimal fixture reproducing the Code4rena RSC flight-data
// format observed via go-wowa render (2026-05-20). Code4rena uses Next.js App Router;
// the page embeds escaped JSON in the HTML with single-backslash-escaped quotes.
// Active statuses include LiveJudging, Judging, Reporting. "Completed" is excluded.
const sampleCode4renaHTML = `<html><body>
<script>
0a:{\"auditType\":\"Audit\",\"botRaceTimeLimit\":1,\"codeAccess\":\"public\",\"formattedAmount\":\"$$135,000 in USDC\",\"slug\":\"2026-04-k2\",\"startTime\":\"2026-04-17T20:00:00.000Z\",\"status\":\"LiveJudging\",\"title\":\"K2\"}
1a:{\"auditType\":\"Audit\",\"formattedAmount\":\"$$22,000 in USDC\",\"slug\":\"2026-04-monetrix\",\"status\":\"Judging\",\"title\":\"Monetrix\"}
2a:{\"auditType\":\"Audit\",\"formattedAmount\":\"$$40,000 in USDC\",\"slug\":\"2025-12-old-protocol\",\"status\":\"Completed\",\"title\":\"Old Protocol\"}
</script>
<a href="/audits/2026-04-k2">K2</a>
<a href="/audits/2026-04-monetrix">Monetrix</a>
<a href="/audits/2025-12-old-protocol">Old Protocol</a>
</body></html>`

// TestCode4renaParseNextData verifies that parseCode4renaHTML extracts only active
// contests and correctly maps slug→title→maxBounty.
func TestCode4renaParseNextData(t *testing.T) {
	programs := parseCode4renaHTML(sampleCode4renaHTML)
	if len(programs) < 1 {
		t.Fatalf("want >= 1 program, got 0")
	}

	// "Completed" status must be excluded.
	for _, p := range programs {
		if strings.Contains(p.Name, "Old Protocol") {
			t.Errorf("completed contest leaked into results: %+v", p)
		}
	}

	// First two entries should be active.
	if len(programs) != 2 {
		t.Errorf("want 2 active programs, got %d: %v", len(programs), programs)
	}

	p := programs[0]
	if p.Platform != "code4rena" {
		t.Errorf("Platform = %q, want code4rena", p.Platform)
	}
	if p.Type != "audit_contest" {
		t.Errorf("Type = %q, want audit_contest", p.Type)
	}
	if !p.Managed {
		t.Errorf("Managed = false, want true")
	}
	if !strings.Contains(p.URL, "code4rena.com/audits/") {
		t.Errorf("URL = %q, want code4rena.com/audits/...", p.URL)
	}
	if p.Name == "" {
		t.Errorf("Name is empty")
	}
	// MaxBounty should be normalised (leading $$ → $)
	if p.MaxBounty != "" && strings.HasPrefix(p.MaxBounty, "$$") {
		t.Errorf("MaxBounty = %q starts with $$, want single $", p.MaxBounty)
	}
}

// TestSearchCode4rena_HTTPMock verifies that SearchCode4rena calls go-wowa render
// and returns active programs from mock HTML.
func TestSearchCode4rena_HTTPMock(t *testing.T) {
	renderPayload, _ := json.Marshal(goWowaRenderResp{
		URL:  "https://code4rena.com/audits",
		HTML: sampleCode4renaHTML,
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(renderPayload)
	}))
	defer srv.Close()

	orig := goWowaRenderURL
	goWowaRenderURL = srv.URL
	defer func() { goWowaRenderURL = orig }()

	origKey := code4renaCacheKey
	code4renaCacheKey = "code4rena_test_mock"
	defer func() { code4renaCacheKey = origKey }()

	ctx := context.Background()
	programs, err := SearchCode4rena(ctx, 10)
	if err != nil {
		t.Fatalf("SearchCode4rena: unexpected error: %v", err)
	}
	if len(programs) == 0 {
		t.Fatal("want >= 1 program, got 0")
	}
	for i, p := range programs {
		if p.Platform != "code4rena" {
			t.Errorf("[%d] Platform = %q, want code4rena", i, p.Platform)
		}
		if p.URL == "" {
			t.Errorf("[%d] URL is empty", i)
		}
	}
}

// TestSearchCode4rena_Cache verifies that a second call returns cached data.
func TestSearchCode4rena_Cache(t *testing.T) {
	engine.InitCache("", 15*time.Minute, 200, 5*time.Minute)

	renderPayload, _ := json.Marshal(goWowaRenderResp{
		URL:  "https://code4rena.com/audits",
		HTML: sampleCode4renaHTML,
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(renderPayload)
	}))

	orig := goWowaRenderURL
	goWowaRenderURL = srv.URL
	defer func() { goWowaRenderURL = orig }()

	origKey := code4renaCacheKey
	code4renaCacheKey = "code4rena_test_cache"
	defer func() { code4renaCacheKey = origKey }()

	ctx := context.Background()

	// First call — populates cache.
	first, err := SearchCode4rena(ctx, 10)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}

	// Close server — subsequent HTTP calls would fail.
	srv.Close()
	goWowaRenderURL = "http://127.0.0.1:0"

	// Second call — must succeed from cache.
	second, err := SearchCode4rena(ctx, 10)
	if err != nil {
		t.Fatalf("second call (should be cached): %v", err)
	}
	if len(first) != len(second) {
		t.Errorf("cache mismatch: first=%d second=%d", len(first), len(second))
	}
}

// TestPrettifyC4RSlug verifies slug → display name conversion.
func TestPrettifyC4RSlug(t *testing.T) {
	cases := []struct {
		slug string
		want string
	}{
		{"2026-04-k2", "K2 (2026-04)"},
		{"2025-11-merkl", "Merkl (2025-11)"},
		{"2025-11-hybra-finance", "Hybra Finance (2025-11)"},
		{"bad", "bad"},
	}
	for _, c := range cases {
		got := prettifyC4RSlug(c.slug)
		if got != c.want {
			t.Errorf("prettifyC4RSlug(%q) = %q, want %q", c.slug, got, c.want)
		}
	}
}
