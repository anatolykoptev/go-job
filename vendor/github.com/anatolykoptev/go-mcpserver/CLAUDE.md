# go-mcpserver

Bootstrap library for Go MCP servers. Zero-binary — imported as a dependency.

## What it does

- Stdio detection (`--stdio` flag) with automatic slog routing (stdout HTTP / stderr stdio)
- Signal handling (SIGINT/SIGTERM) with graceful shutdown
- StreamableHTTP handler at `/mcp` via `go-sdk/mcp`
- Middleware chain: Recovery (with stack traces), RequestID, RequestLog (method/path/status/bytes/duration), CORS
- Health endpoints: `/health`, `/health/live`, `/health/ready`
- Optional `/metrics` endpoint
- Config-driven: sensible defaults, env var overrides (`MCP_PORT`)

## File layout

| File | Purpose |
|------|---------|
| `mcpserver.go` | `Run()` + `Build()` entry points, stdio/HTTP branching, server lifecycle |
| `config.go` | `Config` struct, validation, defaults, `buildMiddleware` |
| `auth.go` | `BearerAuth` config (incl. `ToolFilter`), `toolFilterMiddleware`, OAuth 2.1 types |
| `middleware.go` | Recovery, RequestID, RequestLog, CORS, `WithRequestID` context helper |
| `response_writer.go` | `responseWriter` wrapper (status/bytes capture, `http.Flusher`) |
| `health.go` | `/health`, `/health/live`, `/health/ready` handlers |
| `hooks.go` | `MCPHooks` typed callbacks for tool metrics/tracing → `mcp.Middleware` |
| `testing.go` | `NewTestServer(t, server, cfg)` — httptest helper with auto-cleanup |

## Commands

```bash
make test    # go test -v -race -count=1 ./...
make lint    # golangci-lint run ./...
go vet ./... # additional checks
```

## Consumers

- `gigiena-teksta` — anglicism checker MCP server
- `go-wp` — WordPress MCP server
- `go-content` — multi-tenant intelligence engine
- `go-billing` — license & billing service
- `go-code` — code intelligence MCP server
- `go-hully` — crypto twitter intelligence
- `go-search` — web search MCP server
- `go-job` — job search MCP server
- `go-startup` — startup tools MCP server

## Key decisions

- **Library, not binary** — no `main.go`, no GoReleaser; consumers `go get` it
- **Stateless by default** — `Config.Stateless *bool` (nil=true); `Config.DisableMCP` skips `/mcp` routes
- **OAuth 2.1 bearer auth** — `Config.BearerAuth` wraps `/mcp` only; metadata via RFC 9728
- **Tool scope filtering** — `BearerAuth.ToolFilter` hides/denies tools per token scopes (MCP middleware)
- **MCPHooks** — typed callbacks (`OnToolCall`, `OnToolResult`, `OnError`) for metrics/tracing
- **MCP-layer middleware** — `Config.MCPReceivingMiddleware`/`MCPSendingMiddleware` pass-through to go-sdk
- **`Build()` for testing** — returns `http.Handler` without starting server
- **`NewTestServer()`** — httptest helper with auto-cleanup for integration tests
- **Context key pattern** — unexported `requestIDContextKey struct{}` (not int-based)
- **Error channel over os.Exit** — `Run()` returns bind errors instead of calling `os.Exit(1)`
- **No external dependencies** beyond `go-sdk/mcp` (stdlib only for middleware)
