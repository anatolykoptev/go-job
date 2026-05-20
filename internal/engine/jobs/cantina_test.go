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

// sampleCantinaHTML is a minimal fixture that reproduces the Cantina App Router DOM
// structure observed via go-wowa render (2026-05-20).
// Cantina uses Next.js App Router — no __NEXT_DATA__; competition cards render as
// <a title="TITLE" href="/competitions/UUID"> anchors with $N,NNN prize text.
const sampleCantinaHTML = `<html><body>
<div class="competition-list">
  <a class="chakra-stack css-1gysfy9"
     title="Revert Finance / Revert Finance - StableSwap Hooks"
     href="/competitions/e55ee7b9-6c99-42f8-8338-39f3dd134ef3">
    <div>Revert Finance</div>
    <span>$50,000</span>
  </a>
  <a class="chakra-stack css-1gysfy9"
     title="Reserve Protocol / Reserve Governor"
     href="/competitions/980a5976-9a7d-4014-b2e1-c248b4c6fa44">
    <div>Reserve Protocol</div>
    <span>$30,000</span>
  </a>
</div>
</body></html>`

// TestCantinaParseNextData verifies that parseCantinaHTML extracts contests from
// the fixture HTML (title attr + UUID href pattern confirmed via live recon).
func TestCantinaParseNextData(t *testing.T) {
	programs := parseCantinaHTML(sampleCantinaHTML)
	if len(programs) < 1 {
		t.Fatalf("want >= 1 program, got 0")
	}
	// First entry
	p := programs[0]
	if p.Platform != "cantina" {
		t.Errorf("Platform = %q, want %q", p.Platform, "cantina")
	}
	if p.Type != "audit_contest" {
		t.Errorf("Type = %q, want %q", p.Type, "audit_contest")
	}
	if !p.Managed {
		t.Errorf("Managed = false, want true")
	}
	if !strings.Contains(p.URL, "cantina.xyz/competitions/") {
		t.Errorf("URL = %q, want cantina.xyz/competitions/...", p.URL)
	}
	if p.Name == "" {
		t.Errorf("Name is empty")
	}
	if p.MaxBounty == "" {
		t.Errorf("MaxBounty is empty for contest with $50,000")
	}

	// Verify both entries extracted.
	if len(programs) != 2 {
		t.Errorf("want 2 programs, got %d", len(programs))
	}
	if programs[1].MaxBounty != "$30,000" {
		t.Errorf("programs[1].MaxBounty = %q, want %q", programs[1].MaxBounty, "$30,000")
	}
}

// TestSearchCantina_HTTPMock verifies that SearchCantina calls go-wowa render
// and returns at least one program from mock HTML.
func TestSearchCantina_HTTPMock(t *testing.T) {
	// Mock go-wowa render endpoint. Use json.Marshal to safely encode the HTML.
	renderPayload, _ := json.Marshal(goWowaRenderResp{URL: "https://cantina.xyz/competitions", HTML: sampleCantinaHTML})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(renderPayload)
	}))
	defer srv.Close()

	orig := goWowaRenderURL
	goWowaRenderURL = srv.URL
	defer func() { goWowaRenderURL = orig }()

	origKey := cantinaCacheKey
	cantinaCacheKey = "cantina_test_mock"
	defer func() { cantinaCacheKey = origKey }()

	ctx := context.Background()
	programs, err := SearchCantina(ctx, 10)
	if err != nil {
		t.Fatalf("SearchCantina: unexpected error: %v", err)
	}
	if len(programs) == 0 {
		t.Fatal("want >= 1 program, got 0")
	}
	for i, p := range programs {
		if p.Platform != "cantina" {
			t.Errorf("[%d] Platform = %q, want cantina", i, p.Platform)
		}
		if p.URL == "" {
			t.Errorf("[%d] URL is empty", i)
		}
	}
}

// TestSearchCantina_Cache verifies that a second call returns cached data.
func TestSearchCantina_Cache(t *testing.T) {
	engine.InitCache("", 15*time.Minute, 200, 5*time.Minute)

	renderPayload, _ := json.Marshal(goWowaRenderResp{URL: "https://cantina.xyz/competitions", HTML: sampleCantinaHTML})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(renderPayload)
	}))

	orig := goWowaRenderURL
	goWowaRenderURL = srv.URL
	defer func() { goWowaRenderURL = orig }()

	origKey := cantinaCacheKey
	cantinaCacheKey = "cantina_test_cache"
	defer func() { cantinaCacheKey = origKey }()

	ctx := context.Background()

	// First call — populates cache.
	first, err := SearchCantina(ctx, 10)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}

	// Close server — subsequent HTTP calls would fail.
	srv.Close()
	goWowaRenderURL = "http://127.0.0.1:0"

	// Second call — must succeed from cache.
	second, err := SearchCantina(ctx, 10)
	if err != nil {
		t.Fatalf("second call (should be cached): %v", err)
	}
	if len(first) != len(second) {
		t.Errorf("cache mismatch: first=%d second=%d", len(first), len(second))
	}
}
