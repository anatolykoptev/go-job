// Package mcpmw provides per-tool metrics middleware for MCP servers.
//
// It implements [mcp.Middleware] so callers can plug it directly into
// [mcpserver.Config.MCPReceivingMiddleware]:
//
//	MCPReceivingMiddleware: []mcp.Middleware{
//	    hooks.Middleware(),
//	    mcpmw.Middleware(reg, "tool"),
//	},
//
// Metrics recorded per tool call:
//
//	<subsystem>_calls_total{tool,status}   — status ∈ {"ok","error"}
//	<subsystem>_duration_seconds{tool}     — gauge (seconds)
package mcpmw

import (
	"context"
	"time"

	"github.com/anatolykoptev/go-kit/metrics"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const toolsCallMethod = "tools/call"

// Middleware returns an [mcp.Middleware] that records RED metrics for every
// tools/call invocation. Non-tool methods are passed through unmodified.
//
// Metrics are written into reg using [metrics.Label]:
//   - <subsystem>_calls_total{tool="<name>",status="ok"|"error"}
//   - <subsystem>_duration_seconds{tool="<name>"}
//
// Nil-safe: if reg is nil, the middleware is installed but no metrics are
// recorded (handler is still called normally).
func Middleware(reg *metrics.Registry, subsystem string) mcp.Middleware {
	callsName := subsystem + "_calls_total"
	durName := subsystem + "_duration_seconds"

	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method != toolsCallMethod {
				return next(ctx, method, req)
			}

			toolName := ""
			if req != nil {
				if p, ok := req.GetParams().(*mcp.CallToolParamsRaw); ok && p != nil {
					toolName = p.Name
				}
			}

			start := time.Now()
			result, err := next(ctx, method, req)
			elapsed := time.Since(start)

			status := "ok"
			if err != nil {
				status = "error"
			} else if cr, ok := result.(*mcp.CallToolResult); ok && cr != nil && cr.IsError {
				status = "error"
			}

			reg.Incr(metrics.Label(callsName, "tool", toolName, "status", status))
			reg.Gauge(metrics.Label(durName, "tool", toolName)).Set(elapsed.Seconds())

			return result, err
		}
	}
}
