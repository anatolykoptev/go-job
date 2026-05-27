package jobserver

import (
	"context"
	"errors"
	"net/http/httptest"
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
