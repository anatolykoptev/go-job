package jobserver

import (
	"context"
	"testing"

	linkedin "github.com/anatolykoptev/go-linkedin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// linkedInToolClient creates an in-memory MCP client connected to a server
// that has only the linkedin tool registered. The returned cleanup function
// must be deferred by the caller.
func linkedInToolClient(t *testing.T) (*mcp.ClientSession, context.Context, func()) {
	t.Helper()

	srv := mcp.NewServer(&mcp.Implementation{Name: "go-job-test", Version: "1.0"}, nil)
	registerLinkedIn(srv)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = srv.Run(ctx, serverTransport) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		cancel()
		t.Fatalf("client connect: %v", err)
	}

	cleanup := func() {
		_ = cs.Close()
		cancel()
	}
	return cs, ctx, cleanup
}

// TestLinkedInJobs_EnrichParamsPassed verifies the MCP tool forwards
// enrich=true and enrich_limit=N into linkedin.JobSearchParams.
func TestLinkedInJobs_EnrichParamsPassed(t *testing.T) {
	var captured linkedin.JobSearchParams
	orig := voyagerJobs
	voyagerJobs = func(ctx context.Context, params linkedin.JobSearchParams) ([]linkedin.Job, error) {
		captured = params
		return []linkedin.Job{{Title: "Go Dev", Company: "Acme", URN: "urn:li:fsd_jobPosting:123"}}, nil
	}
	t.Cleanup(func() { voyagerJobs = orig })

	cs, ctx, cleanup := linkedInToolClient(t)
	defer cleanup()

	_, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "linkedin",
		Arguments: map[string]any{
			"op":           "jobs",
			"query":        "golang",
			"location":     "Remote",
			"remote":       "remote",
			"limit":        5,
			"enrich":       true,
			"enrich_limit": 3,
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	if !captured.Enrich {
		t.Fatalf("Enrich: got false, want true")
	}
	if captured.EnrichLimit != 3 {
		t.Fatalf("EnrichLimit: got %d, want 3", captured.EnrichLimit)
	}
	if captured.Query != "golang" || captured.Location != "Remote" || captured.Remote != "remote" || captured.Limit != 5 {
		t.Fatalf("core params not preserved: %+v", captured)
	}
}

// TestLinkedInJobs_EnrichDefaultOff verifies omitting enrich/enrich_limit
// leaves Enrich=false and EnrichLimit=0, preserving the current single-call
// behavior.
func TestLinkedInJobs_EnrichDefaultOff(t *testing.T) {
	var captured linkedin.JobSearchParams
	orig := voyagerJobs
	voyagerJobs = func(ctx context.Context, params linkedin.JobSearchParams) ([]linkedin.Job, error) {
		captured = params
		return []linkedin.Job{{Title: "Go Dev", Company: "Acme", URN: "urn:li:fsd_jobPosting:123"}}, nil
	}
	t.Cleanup(func() { voyagerJobs = orig })

	cs, ctx, cleanup := linkedInToolClient(t)
	defer cleanup()

	_, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "linkedin",
		Arguments: map[string]any{
			"op":    "jobs",
			"query": "golang",
			"limit": 5,
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	if captured.Enrich {
		t.Fatalf("Enrich: got true, want false (default-off)")
	}
	if captured.EnrichLimit != 0 {
		t.Fatalf("EnrichLimit: got %d, want 0", captured.EnrichLimit)
	}
}
