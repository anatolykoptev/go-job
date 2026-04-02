package jobserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	linkedin "github.com/anatolykoptev/go-linkedin"
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
	nervURL := os.Getenv("GO_NERV_URL")
	if nervURL == "" {
		nervURL = "http://go-nerv:8895"
	}

	payload, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "nerv_linkedin_ingest",
			"arguments": map[string]any{
				"tenant_id": tenantID,
				"profile":   profile,
			},
		},
	})

	ctx, cancel := context.WithTimeout(ctx, nervIngestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, nervURL+"/mcp", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("nerv request: %w", err)
	}
	defer resp.Body.Close()

	// go-nerv uses SSE transport: "event: message\ndata: {json}\n\n"
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read nerv response: %w", err)
	}
	// Extract JSON from SSE "data: " line
	jsonData := body
	if idx := bytes.Index(body, []byte("data: ")); idx >= 0 {
		jsonData = body[idx+6:]
		if end := bytes.Index(jsonData, []byte("\n")); end >= 0 {
			jsonData = jsonData[:end]
		}
	}
	var rpcResp struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(jsonData, &rpcResp); err != nil {
		return nil, fmt.Errorf("decode nerv response: %w (body: %s)", err, string(body[:min(200, len(body))]))
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("nerv: %s", rpcResp.Error.Message)
	}
	return rpcResp.Result, nil
}
