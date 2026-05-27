package jobserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	linkedin "github.com/anatolykoptev/go-linkedin"
	mcpserver "github.com/anatolykoptev/go-mcpserver"
	"github.com/anatolykoptev/go-mcpserver/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// newNervStub builds a minimal MCP httptest.Server exposing a
// "nerv_linkedin_ingest" tool that returns a fixed JSON payload.
func newNervStub(t *testing.T) *httptest.Server {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "go-nerv-stub", Version: "1.0"}, nil)

	type ingestArgs struct {
		TenantID string          `json:"tenant_id"`
		Profile  *linkedin.Profile `json:"profile"`
	}
	mcpserver.AddTool(srv, &mcp.Tool{
		Name:        "nerv_linkedin_ingest",
		Description: "stub: acknowledges ingest",
	}, func(_ context.Context, _ *mcp.CallToolRequest, args ingestArgs) (*mcp.CallToolResult, error) {
		msg := "ingested for tenant: " + args.TenantID
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: msg}},
		}, nil
	})

	ts := mcpserver.NewTestServer(t, srv, mcpserver.Config{Name: "go-nerv-stub", Version: "1.0.0"})
	return ts
}

// TestSendToNerv_RoundTrip verifies sendToNerv reaches the stub server and
// returns a non-nil json.RawMessage.
func TestSendToNerv_RoundTrip(t *testing.T) {
	ts := newNervStub(t)
	t.Setenv("GO_NERV_URL", ts.URL)

	profile := &linkedin.Profile{FirstName: "Test", LastName: "User"}
	raw, err := sendToNerv(context.Background(), "startup", profile)
	if err != nil {
		t.Fatalf("sendToNerv: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("sendToNerv returned empty json.RawMessage")
	}
}

// TestSendToNerv_Unavailable verifies that when go-nerv is unreachable,
// sendToNerv returns a non-nil error. This is critical: the caller
// (registerLinkedInProfileIngest) branches on err!=nil to log a warning and
// return NervIngested=false. WithUnreachableTolerant is NOT set, so
// ErrUnreachable propagates instead of being silently swallowed.
func TestSendToNerv_Unavailable(t *testing.T) {
	t.Setenv("GO_NERV_URL", "http://127.0.0.1:1")

	profile := &linkedin.Profile{FirstName: "Test", LastName: "User"}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err := sendToNerv(ctx, "startup", profile)
	if err == nil {
		t.Fatal("expected error when go-nerv is unreachable, got nil")
	}
	if !errors.Is(err, mcpclient.ErrUnreachable) {
		t.Fatalf("want ErrUnreachable in chain, got %T: %v", err, err)
	}
}

// TestNervClientURL_Default verifies the default URL when env var is unset.
func TestNervClientURL_Default(t *testing.T) {
	t.Setenv("GO_NERV_URL", "")
	got := nervClientURL()
	const want = "http://go-nerv:8895"
	if got != want {
		t.Errorf("nervClientURL() = %q, want %q", got, want)
	}
}

// TestNervClientURL_EnvOverride verifies env var override.
func TestNervClientURL_EnvOverride(t *testing.T) {
	t.Setenv("GO_NERV_URL", "http://custom-nerv:9000")
	got := nervClientURL()
	const want = "http://custom-nerv:9000"
	if got != want {
		t.Errorf("nervClientURL() = %q, want %q", got, want)
	}
}

// TestSendToNerv_Timeout verifies sendToNerv respects nervIngestTimeout.
// Uses a stall listener that accepts connections but never responds.
func TestSendToNerv_Timeout(t *testing.T) {
	// Build a stall server that accepts but never writes.
	stallSrv := httptest.NewServer(nil)
	stallSrv.Close() // close immediately — further dials will be refused
	t.Setenv("GO_NERV_URL", "http://127.0.0.1:1")

	profile := &linkedin.Profile{FirstName: "Timeout", LastName: "Test"}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := sendToNerv(ctx, "startup", profile)
	if err == nil {
		t.Fatal("expected timeout/unreachable error, got nil")
	}
}

// headerCapture wraps an http.Handler and records every Authorization header
// received across all requests. Thread-safe via mu.
type headerCapture struct {
	mu      sync.Mutex
	headers []string
	next    http.Handler
}

func (hc *headerCapture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	hc.mu.Lock()
	hc.headers = append(hc.headers, r.Header.Get("Authorization"))
	hc.mu.Unlock()
	hc.next.ServeHTTP(w, r)
}

func (hc *headerCapture) captured() []string {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	out := make([]string, len(hc.headers))
	copy(out, hc.headers)
	return out
}

// newNervStubWithCapture builds a nerv stub httptest.Server that records all
// Authorization headers so tests can assert bearer token delivery.
func newNervStubWithCapture(t *testing.T) (*httptest.Server, *headerCapture) {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "go-nerv-stub-capture", Version: "1.0"}, nil)

	type ingestArgs struct {
		TenantID string            `json:"tenant_id"`
		Profile  *linkedin.Profile `json:"profile"`
	}
	mcpserver.AddTool(srv, &mcp.Tool{
		Name:        "nerv_linkedin_ingest",
		Description: "stub: acknowledges ingest (capture variant)",
	}, func(_ context.Context, _ *mcp.CallToolRequest, args ingestArgs) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "ok"}},
		}, nil
	})

	inner, err := mcpserver.Build(srv, mcpserver.Config{Name: "go-nerv-stub-capture", Version: "1.0.0"})
	if err != nil {
		t.Fatalf("mcpserver.Build: %v", err)
	}

	hc := &headerCapture{next: inner}
	ts := httptest.NewServer(hc)
	t.Cleanup(ts.Close)
	return ts, hc
}

// TestSendToNerv_BearerToken verifies that when NERV_MCP_TOKEN is set,
// sendToNerv sends "Authorization: Bearer <token>" on every HTTP request to
// go-nerv. Empty token must NOT send a header (safe before enforcement).
//
// RED: fails before mcpclient.WithBearer(os.Getenv("NERV_MCP_TOKEN")) is added.
// GREEN: passes after the option is wired in sendToNerv.
func TestSendToNerv_BearerToken(t *testing.T) {
	ts, hc := newNervStubWithCapture(t)
	const token = "test-nerv-token-abc123"
	t.Setenv("GO_NERV_URL", ts.URL)
	t.Setenv("NERV_MCP_TOKEN", token)

	profile := &linkedin.Profile{FirstName: "Bearer", LastName: "Test"}
	_, err := sendToNerv(context.Background(), "startup", profile)
	if err != nil {
		t.Fatalf("sendToNerv: %v", err)
	}

	headers := hc.captured()
	if len(headers) == 0 {
		t.Fatal("no requests reached the stub — cannot assert Authorization header")
	}
	want := "Bearer " + token
	for i, h := range headers {
		if h != want {
			t.Errorf("request[%d] Authorization = %q, want %q", i, h, want)
		}
	}
}

// TestSendToNerv_NoTokenNoHeader verifies that when NERV_MCP_TOKEN is empty
// (default), no Authorization header is sent. Safe before enforcement.
func TestSendToNerv_NoTokenNoHeader(t *testing.T) {
	ts, hc := newNervStubWithCapture(t)
	t.Setenv("GO_NERV_URL", ts.URL)
	t.Setenv("NERV_MCP_TOKEN", "")

	profile := &linkedin.Profile{FirstName: "NoToken", LastName: "Test"}
	_, err := sendToNerv(context.Background(), "startup", profile)
	if err != nil {
		t.Fatalf("sendToNerv: %v", err)
	}

	headers := hc.captured()
	if len(headers) == 0 {
		t.Fatal("no requests reached the stub — cannot assert absence of Authorization header")
	}
	for i, h := range headers {
		if h != "" {
			t.Errorf("request[%d] Authorization = %q, want empty (no token set)", i, h)
		}
	}
}
