package jobs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// Fixture mirrors the real careers.un.org filteredV2 response shape captured
// 2026-05-28 — two jobs, one with empty postingTitle (falls back to jobTitle),
// one with full metadata.
const inspiraFixture = `{
  "status": 1,
  "message": "Success.",
  "data": {
    "list": [
      {
        "_id": "6a186a8116675b6a62702075",
        "jobId": 277561,
        "language": "EN",
        "categoryCode": "CON",
        "jobTitle": "Individual Contractors (2 positions) — system automation",
        "postingTitle": "",
        "jobDescription": "<div class='jobPostingDetail'>Result of Service Output 1: Integrated SISO V2 Architecture</div>",
        "dutyStation": [{"code":"0300","description":"VIENNA"}],
        "startDate": "2026-05-28T04:00:00.000Z",
        "endDate":   "2026-06-08T03:59:59.000Z",
        "jc":   {"code":"CON","name":"Consultants"},
        "jl":   {"code":"CON","name":"CON"},
        "dept": {"code":"47404740","name":"United Nations Office on Drugs and Crime"},
        "totalCount": 409
      },
      {
        "_id": "6a18000016675b6a62700001",
        "jobId": 277335,
        "language": "EN",
        "categoryCode": "GEN",
        "jobTitle": "INFORMATION SYSTEMS ASSISTANT/WEB DEVELOPER",
        "postingTitle": "INFORMATION SYSTEMS ASSISTANT/WEB DEVELOPER (Temporary Job Opening), G6",
        "jobDescription": "",
        "dutyStation": [{"code":"0500","description":"GENEVA"}],
        "startDate": "2026-05-28T00:00:00.000Z",
        "endDate":   "2026-06-05T00:00:00.000Z",
        "jc":   {"code":"GEN","name":"General Service and Related Categories"},
        "jl":   {"code":"G6","name":"G-6"},
        "dept": {"code":"00010001","name":"United Nations Conference on Trade and Development"}
      }
    ]
  }
}`

func TestSearchInspiraJobs_Fixture(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected JSON content-type, got %q", ct)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(inspiraFixture))
	}))
	t.Cleanup(srv.Close)

	// Swap the API URL for the test server via the package-level constant.
	// We can't const-override, so use a temporary HTTP client redirect.
	origClient := engine.Cfg.HTTPClient
	engine.Cfg.HTTPClient = &http.Client{
		Timeout: 10 * time.Second,
		Transport: &redirectTransport{target: srv.URL},
	}
	t.Cleanup(func() { engine.Cfg.HTTPClient = origClient })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out, err := SearchInspiraJobs(ctx, "system", "", 10)
	if err != nil {
		t.Fatalf("SearchInspiraJobs: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(out))
	}

	// Job 1: postingTitle empty → falls back to jobTitle.
	j1 := out[0]
	if !strings.Contains(j1.Title, "system automation") {
		t.Errorf("[0] title fell back wrong: %q", j1.Title)
	}
	if !strings.HasSuffix(j1.URL, "/jobSearchDescription/277561") {
		t.Errorf("[0] URL: %q", j1.URL)
	}
	if !strings.Contains(j1.Content, "United Nations Office on Drugs and Crime") {
		t.Errorf("[0] dept missing from content")
	}
	if !strings.Contains(j1.Content, "Vienna") {
		t.Errorf("[0] duty station not title-cased: %q", j1.Content)
	}
	if j1.Metadata["source"] != "inspira" {
		t.Errorf("[0] metadata source wrong: %q", j1.Metadata["source"])
	}

	// Job 2: full postingTitle wins over jobTitle.
	j2 := out[1]
	if !strings.Contains(j2.Title, "G6") {
		t.Errorf("[1] expected postingTitle (with G6 suffix), got %q", j2.Title)
	}
	if !strings.HasSuffix(j2.URL, "/jobSearchDescription/277335") {
		t.Errorf("[1] URL: %q", j2.URL)
	}
}

// TestSearchInspiraJobs_ContextCancel verifies that SearchInspiraJobs respects
// context cancellation — a stalling careers.un.org does not block past the
// deadline. This guards the inspira timeout fix (P4): the per-source timeout
// added to runSource propagates via context, so SearchInspiraJobs must honour
// its ctx.
//
// Revert-red: removing context cancellation from the net/http request (e.g.
// using context.Background() instead of ctx in the HTTP request) would cause
// this test to fail: SearchInspiraJobs would block well past the 200ms deadline.
func TestSearchInspiraJobs_ContextCancel(t *testing.T) {
	// done is closed by t.Cleanup so the handler goroutine can exit cleanly.
	done := make(chan struct{})
	// Stalling server: blocks until we signal it to stop (via done), simulating
	// a slow careers.un.org endpoint.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Wait for the test to finish (or for the server to be closed).
		select {
		case <-done:
		case <-time.After(30 * time.Second): // safety valve
		}
		http.Error(w, "server done", http.StatusServiceUnavailable)
	}))
	t.Cleanup(func() {
		close(done)
		srv.CloseClientConnections()
		srv.Close()
	})

	origClient := engine.Cfg.HTTPClient
	engine.Cfg.HTTPClient = &http.Client{
		// No timeout on the HTTP client itself — context cancellation is what
		// should stop the call (the fix we're testing).
		Transport: &redirectTransport{target: srv.URL},
	}
	t.Cleanup(func() { engine.Cfg.HTTPClient = origClient })

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := SearchInspiraJobs(ctx, "engineer", "", 5)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("SearchInspiraJobs: expected error on context cancel, got nil")
	}
	// Must return promptly after the context deadline — not hang for seconds.
	if elapsed > 3*time.Second {
		t.Errorf("SearchInspiraJobs took %s after context cancel — did not respect deadline "+
			"(hint: the HTTP request must use the passed ctx)", elapsed)
	}
	t.Logf("SearchInspiraJobs returned after %s with err=%v (expected)", elapsed, err)
}

// redirectTransport rewrites any outbound request to point at the test server.
type redirectTransport struct {
	target string
}

func (rt *redirectTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	// Replace scheme+host with the test server while keeping the path so the
	// scraper's request URL would still hit the httptest mux even if it later
	// adds query strings.
	r.URL.Scheme = "http"
	target := strings.TrimPrefix(rt.target, "http://")
	r.URL.Host = target
	return http.DefaultTransport.RoundTrip(r)
}
