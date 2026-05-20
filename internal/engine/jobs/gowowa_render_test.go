package jobs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestFetchRenderedHTML_MockServer verifies that fetchRenderedHTML correctly calls
// the go-wowa render endpoint and returns the HTML field from the response.
func TestFetchRenderedHTML_MockServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("want POST, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("want Content-Type application/json, got %s", ct)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"url":"https://example.com","html":"<html><body>hello</body></html>"}`))
	}))
	defer srv.Close()

	orig := goWowaRenderURL
	goWowaRenderURL = srv.URL
	defer func() { goWowaRenderURL = orig }()

	html, err := fetchRenderedHTML(context.Background(), "https://example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if html != "<html><body>hello</body></html>" {
		t.Errorf("unexpected html: %q", html)
	}
}

// TestFetchRenderedHTML_NonOKStatus verifies that a non-200 response returns an error.
func TestFetchRenderedHTML_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`service unavailable`))
	}))
	defer srv.Close()

	orig := goWowaRenderURL
	goWowaRenderURL = srv.URL
	defer func() { goWowaRenderURL = orig }()

	_, err := fetchRenderedHTML(context.Background(), "https://example.com")
	if err == nil {
		t.Fatal("expected error for non-200 status, got nil")
	}
}

// TestRenderTruncate verifies truncation logic.
func TestRenderTruncate(t *testing.T) {
	if got := renderTruncate("hello", 10); got != "hello" {
		t.Errorf("want %q, got %q", "hello", got)
	}
	if got := renderTruncate("hello world", 5); got != "hello..." {
		t.Errorf("want %q, got %q", "hello...", got)
	}
}
