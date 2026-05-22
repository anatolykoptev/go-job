package jobserver

import (
	"context"
	"log/slog"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/oversize"
)

// maybeSpill wraps an MCP tool output with the oversize spill logic.
//
// If the oversize store is not configured (DATABASE_URL unset), the original
// payload is returned unchanged — graceful degradation.
// If spill fails (marshal or DB error), the original payload is returned with
// a warn log — graceful degradation, never panics.
// On successful spill the envelope replaces the payload and the
// gojob_oversize_spill_total{tool} counter and gojob_oversize_bytes histogram
// are updated.
func maybeSpill(ctx context.Context, toolName string, payload any) any {
	store := engine.GetOversizeStore()
	if store == nil {
		return payload
	}
	spilled, err := oversize.MaybeSpill(ctx, store, toolName, payload)
	if err != nil {
		slog.Warn("oversize spill failed; returning original",
			slog.String("tool", toolName),
			slog.Any("error", err),
		)
		return payload
	}
	if env, ok := spilled.(*oversize.Envelope); ok {
		engine.IncrOversizeSpill(toolName)
		engine.ObserveOversizeBytes(env.SizeBytes)
	}
	return spilled
}
