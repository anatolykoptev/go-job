package jobserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/oversize"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// spillIfOversize marshals payload, checks it against the oversize threshold,
// and returns a *mcp.CallToolResult carrying the envelope if the payload was
// spilled, or (nil, false, nil) if the payload is small enough to return
// directly.
//
// The typed Out parameter T is kept concrete in each handler so the SDK can
// derive outputSchema from it (SDK check: reflect.TypeFor[Out]() != any).
//
// Spill path (returned value ok=true):
//   - Returns (*mcp.CallToolResult, true, nil); caller must return
//     (cr, var zero T, nil) so the concrete Out slot carries the schema type.
//   - The SDK will marshal the zero T value into StructuredContent (schema
//     preserved) and leave Content untouched (we pre-fill it with envelope JSON).
//     Clients read Content[0].Text to find the oversize_id envelope.
//
// No-store path (ok=false, err=nil):
//   - oversize store not configured (DATABASE_URL unset) — graceful degradation.
//   - Caller returns the original typed value directly.
//
// DB-error path (ok=false, err!=nil):
//   - NOTE: this is NOT graceful degradation. The SDK will attempt to marshal
//     the typed Out value returned by the caller; if the caller passes the
//     original (potentially large) value, the response will be large but valid.
//     Callers SHOULD log the error and return the original value on err!=nil.
//
// Marshal-error path (ok=false, err!=nil):
//   - Also not graceful: envelope JSON serialization failed. Same caller advice.
func spillIfOversize[T any](ctx context.Context, toolName string, payload T) (*mcp.CallToolResult, bool, error) {
	store := engine.GetOversizeStore()
	if store == nil {
		return nil, false, nil
	}

	spilled, err := oversize.MaybeSpill(ctx, store, toolName, payload)
	if err != nil {
		return nil, false, err
	}

	env, ok := spilled.(*oversize.Envelope)
	if !ok {
		// Payload was below threshold — return without spill.
		return nil, false, nil
	}

	engine.IncrOversizeSpill(toolName)
	engine.AddOversizeBytes(int64(env.SizeBytes))

	raw, mErr := json.Marshal(env)
	if mErr != nil {
		return nil, false, fmt.Errorf("spillIfOversize: marshal envelope: %w", mErr)
	}

	cr := &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}},
	}
	return cr, true, nil
}

// handleSpill is a convenience wrapper around spillIfOversize that absorbs
// DB/marshal errors with a warn log and falls back to returning the original
// value via the caller's return statement.  It is NOT suitable for callers that
// need to distinguish between spill and no-spill — use spillIfOversize directly.
//
// Usage pattern:
//
//	if cr, spilled := handleSpill(ctx, "tool_name", out); spilled {
//	    var zero ConcreteT
//	    return cr, zero, nil
//	}
//	return nil, out, nil
func handleSpill[T any](ctx context.Context, toolName string, payload T) (*mcp.CallToolResult, bool) {
	cr, spilled, err := spillIfOversize(ctx, toolName, payload)
	if err != nil {
		slog.Warn("oversize spill failed; returning original",
			slog.String("tool", toolName),
			slog.Any("error", err),
		)
		return nil, false
	}
	return cr, spilled
}
