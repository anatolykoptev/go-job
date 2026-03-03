package mcpserver

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPHooks provides typed callbacks for common MCP lifecycle events.
// Use [MCPHooks.Middleware] to convert to [mcp.Middleware] for
// [Config.MCPReceivingMiddleware].
//
// Hooks are observer-only — they cannot modify requests or responses.
type MCPHooks struct {
	// OnToolCall fires before a tool executes. toolName is the requested tool.
	OnToolCall func(ctx context.Context, toolName string)
	// OnToolResult fires after a tool executes with timing and outcome.
	OnToolResult func(ctx context.Context, toolName string, duration time.Duration, isError bool)
	// OnError fires when any MCP method returns an error.
	OnError func(ctx context.Context, method string, err error)
}

// Middleware converts MCPHooks to [mcp.Middleware] for use in
// [Config.MCPReceivingMiddleware].
func (h MCPHooks) Middleware() mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "tools/call" {
				return h.handleToolCall(ctx, method, req, next)
			}
			result, err := next(ctx, method, req)
			if err != nil && h.OnError != nil {
				h.OnError(ctx, method, err)
			}
			return result, err
		}
	}
}

func (h MCPHooks) handleToolCall(
	ctx context.Context, method string, req mcp.Request, next mcp.MethodHandler,
) (mcp.Result, error) {
	name := req.GetParams().(*mcp.CallToolParamsRaw).Name
	if h.OnToolCall != nil {
		h.OnToolCall(ctx, name)
	}
	start := time.Now()
	result, err := next(ctx, method, req)
	if h.OnToolResult != nil {
		isErr := err != nil
		if !isErr {
			if cr, ok := result.(*mcp.CallToolResult); ok {
				isErr = cr.IsError
			}
		}
		h.OnToolResult(ctx, name, time.Since(start), isErr)
	}
	if err != nil && h.OnError != nil {
		h.OnError(ctx, method, err)
	}
	return result, err
}
