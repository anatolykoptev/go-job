package jobs

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	mcpserver "github.com/anatolykoptev/go-mcpserver"
	"github.com/anatolykoptev/go-mcpserver/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// newHullyStub builds a minimal MCP httptest.Server exposing an "analyze_account"
// tool that echoes the username back as text content.
func newHullyStub(t *testing.T) *httptest.Server {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "go-hully-stub", Version: "1.0"}, nil)

	type analyzeArgs struct {
		Username string `json:"username"`
	}
	mcpserver.AddTool(srv, &mcp.Tool{
		Name:        "analyze_account",
		Description: "stub: echoes username",
	}, func(_ context.Context, _ *mcp.CallToolRequest, args analyzeArgs) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "stub analysis for @" + args.Username}},
		}, nil
	})

	ts := mcpserver.NewTestServer(t, srv, mcpserver.Config{Name: "go-hully-stub", Version: "1.0.0"})
	return ts
}

// TestCallGoHully_RoundTrip verifies callGoHully reaches the stub server and
// returns text content. Uses the package-level hullyClient var overridden for
// testing.
func TestCallGoHully_RoundTrip(t *testing.T) {
	ts := newHullyStub(t)

	orig := hullyClient
	hullyClient = mcpclient.New(ts.URL, mcpclient.WithTimeout(5*time.Second))
	t.Cleanup(func() {
		_ = hullyClient.Close()
		hullyClient = orig
	})

	got, err := callGoHully(context.Background(), "analyze_account", map[string]any{"username": "testuser"})
	if err != nil {
		t.Fatalf("callGoHully: %v", err)
	}
	if got != "stub analysis for @testuser" {
		t.Fatalf("want %q, got %q", "stub analysis for @testuser", got)
	}
}

// TestCallGoHully_Unavailable verifies that when go-hully is unreachable,
// callGoHully returns a non-nil error. This is critical: the caller in
// ResearchPerson branches on err!=nil to record "(analysis failed)" instead
// of silently emitting an empty result. WithUnreachableTolerant is NOT set on
// hullyClient, so ErrUnreachable propagates.
func TestCallGoHully_Unavailable(t *testing.T) {
	orig := hullyClient
	// Point at a port that is guaranteed to refuse connections.
	hullyClient = mcpclient.New("http://127.0.0.1:1", mcpclient.WithTimeout(2*time.Second))
	t.Cleanup(func() {
		_ = hullyClient.Close()
		hullyClient = orig
	})

	_, err := callGoHully(context.Background(), "analyze_account", map[string]any{"username": "x"})
	if err == nil {
		t.Fatal("expected error when go-hully is unreachable, got nil")
	}
	if !errors.Is(err, mcpclient.ErrUnreachable) {
		t.Fatalf("want ErrUnreachable, got %T: %v", err, err)
	}
}

// TestExtractTwitterHandle covers the URL parsing helper unchanged by migration.
func TestExtractTwitterHandle(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"https://twitter.com/johndoe", "johndoe"},
		{"https://x.com/janedoe?ref=web", "janedoe"},
		{"https://twitter.com/@alice", "alice"},
		{"https://github.com/bob", ""},
		{"", ""},
		{"https://twitter.com/", ""},
		{"https://x.com/too.long.handle.with.dots", ""},
	}
	for _, tt := range tests {
		got := extractTwitterHandle(tt.url)
		if got != tt.want {
			t.Errorf("extractTwitterHandle(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}
