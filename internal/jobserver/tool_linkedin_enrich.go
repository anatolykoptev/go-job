package jobserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	linkedin "github.com/anatolykoptev/go-linkedin"
	"github.com/anatolykoptev/go-mcpserver/mcpclient"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const nervIngestTimeout = 30 * time.Second

type linkedInProfileIngestInput struct {
	Handle   string `json:"handle" jsonschema:"LinkedIn handle or profile URL"`
	TenantID string `json:"tenant_id,omitempty" jsonschema:"go-nerv tenant (default: startup)"`
}

type linkedInProfileIngestOutput struct {
	Profile      *linkedin.Profile `json:"profile"`
	NervIngested bool              `json:"nerv_ingested"`
	NervResult   json.RawMessage   `json:"nerv_result,omitempty"`
}

func nervClientURL() string {
	if u := os.Getenv("GO_NERV_URL"); u != "" {
		return u
	}
	return "http://go-nerv:8895"
}

func registerLinkedInProfileIngest(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "linkedin_profile_ingest",
		Description: "Fetch full LinkedIn profile and save to go-nerv intelligence graph (person, company, skill entities + WORKS_AT/STUDIED_AT edges).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input linkedInProfileIngestInput) (*mcp.CallToolResult, *linkedInProfileIngestOutput, error) {
		if input.Handle == "" {
			return nil, nil, errors.New("handle is required")
		}
		if input.TenantID == "" {
			input.TenantID = "startup"
		}

		profile, err := jobs.VoyagerProfile(ctx, input.Handle)
		if err != nil {
			return nil, nil, fmt.Errorf("fetch profile: %w", err)
		}

		result, err := sendToNerv(ctx, input.TenantID, profile)
		if err != nil {
			slog.Warn("nerv ingestion failed", "handle", input.Handle, "error", err)
			return nil, &linkedInProfileIngestOutput{
				Profile:      profile,
				NervIngested: false,
			}, nil
		}

		return nil, &linkedInProfileIngestOutput{
			Profile:      profile,
			NervIngested: true,
			NervResult:   result,
		}, nil
	})
}

func sendToNerv(ctx context.Context, tenantID string, profile *linkedin.Profile) (json.RawMessage, error) {
	// nervClient is created per-call so it picks up dynamic GO_NERV_URL at
	// runtime. The session is NOT reused (WithSessionReuse(false)) since calls
	// are infrequent and per-call session avoids keeping a long-lived SSE
	// connection open. The caller (registerLinkedInProfileIngest) branches on
	// err!=nil to mark NervIngested=false — WithUnreachableTolerant is NOT set
	// so that unreachable errors surface and the degraded path fires correctly.
	nervClient := mcpclient.New(nervClientURL(),
		mcpclient.WithTimeout(nervIngestTimeout),
		mcpclient.WithSessionReuse(false),
	)
	defer nervClient.Close() //nolint:errcheck

	result, err := nervClient.Call(ctx, "nerv_linkedin_ingest", map[string]any{
		"tenant_id": tenantID,
		"profile":   profile,
	})
	if err != nil {
		return nil, fmt.Errorf("nerv request: %w", err)
	}

	// Marshal the CallToolResult back to JSON for the NervResult pass-through field.
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("nerv encode result: %w", err)
	}
	return json.RawMessage(raw), nil
}
