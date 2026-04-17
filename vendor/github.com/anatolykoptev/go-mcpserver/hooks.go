package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
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

// ToolTimeoutMiddleware returns MCP middleware that enforces tool execution timeouts.
//
// Timeout resolution order (first wins):
//  1. "timeout_secs" in the tool call arguments (LLM per-request override)
//  2. cfg.ToolTimeouts[toolName] (per-tool config)
//  3. cfg.ToolTimeout (global default, 30s)
//
// On timeout the tool returns an error result instead of hanging.
func ToolTimeoutMiddleware(cfg Config) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method != "tools/call" {
				return next(ctx, method, req)
			}
			params := req.GetParams().(*mcp.CallToolParamsRaw)
			timeout := resolveTimeout(params.Name, params.Arguments, cfg)

			ctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			type callResult struct {
				result mcp.Result
				err    error
			}
			ch := make(chan callResult, 1)
			go func() {
				r, e := next(ctx, method, req)
				ch <- callResult{r, e}
			}()

			select {
			case cr := <-ch:
				return cr.result, cr.err
			case <-ctx.Done():
				return &mcp.CallToolResult{
					IsError: true,
					Content: []mcp.Content{&mcp.TextContent{
						Text: fmt.Sprintf("tool %q timed out after %s", params.Name, timeout),
					}},
				}, nil
			}
		}
	}
}

func resolveTimeout(name string, args json.RawMessage, cfg Config) time.Duration {
	// 1. Check request args for timeout_secs.
	if t := parseArgTimeout(args); t > 0 {
		return t
	}

	// 2. Check per-tool config.
	if t, ok := cfg.ToolTimeouts[name]; ok && t > 0 {
		return t
	}

	// 3. Global default.
	return cfg.ToolTimeout
}

func parseArgTimeout(args json.RawMessage) time.Duration {
	if len(args) == 0 {
		return 0
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(args, &m) != nil {
		return 0
	}
	raw, ok := m["timeout_secs"]
	if !ok {
		return 0
	}
	var secs float64
	if json.Unmarshal(raw, &secs) != nil || secs <= 0 {
		return 0
	}
	return time.Duration(secs * float64(time.Second))
}
