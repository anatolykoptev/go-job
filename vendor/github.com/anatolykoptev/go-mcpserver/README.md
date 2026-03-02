# go-mcpserver

Bootstrap library for Go MCP servers. One `Run()` call instead of ~80 lines of boilerplate.

Built on top of [modelcontextprotocol/go-sdk](https://github.com/modelcontextprotocol/go-sdk).

## Install

```bash
go get github.com/anatolykoptev/go-mcpserver@latest
```

## Usage

```go
package main

import (
	mcpserver "github.com/anatolykoptev/go-mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var version = "dev"

func main() {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "my-service",
		Version: version,
	}, nil)

	// register tools...

	if err := mcpserver.Run(server, mcpserver.Config{
		Name:    "my-service",
		Version: version,
	}); err != nil {
		panic(err)
	}
}
```

This gives you:

- **Stdio detection** — pass `--stdio` flag to run via stdin/stdout
- **Structured logging** — slog to stdout (HTTP) or stderr (stdio)
- **Signal handling** — SIGINT/SIGTERM with graceful shutdown
- **MCP routes** — `/mcp` and `/mcp/` with stateless StreamableHTTP
- **Health endpoints** — `/health`, `/health/live`, `/health/ready`
- **Middleware chain** — Recovery, Request ID, Request logging, CORS
- **Config validation** — `Name` and `Version` are required
- **Configurable timeouts** — read (30s), write (120s), shutdown (10s)

## Config

```go
type Config struct {
	Name    string // service name (required)
	Version string // version string (required)
	Port    string // HTTP port; empty → MCP_PORT env → "8080"

	WriteTimeout    time.Duration // default 120s
	ReadTimeout     time.Duration // default 30s
	ShutdownTimeout time.Duration // default 10s

	Metrics func() string        // if set, registers GET /metrics
	Routes  func(*http.ServeMux) // extra routes after /mcp, /health, /metrics

	Middleware        []Middleware  // custom middleware, applied after built-ins
	CORSOrigins       []string     // nil = no CORS; ["*"] = allow all
	CORSMaxAge        int          // preflight Max-Age in seconds; 0 = omit
	CORSAllowHeaders  []string     // nil = default (Content-Type, Authorization, X-Request-ID)
	ReadinessCheck    func() error // nil = /health/ready always returns 200

	DisableRecovery   bool            // default false (recovery ON)
	DisableHealth     bool            // set true to register custom /health in Routes
	DisableRequestLog bool            // default false (request logging ON)

	Context    context.Context // nil → internal signal.NotifyContext(SIGINT, SIGTERM)
	Logger     *slog.Logger    // nil → auto
	OnShutdown func()          // called before HTTP shutdown
}
```

## Health Endpoints

| Endpoint | Purpose |
|----------|---------|
| `GET /health` | Basic health: `{"status":"ok","service":"...","version":"..."}` |
| `GET /health/live` | Liveness probe: always returns 200 |
| `GET /health/ready` | Readiness probe: calls `ReadinessCheck`, returns 200 or 503 |

## Middleware

Built-in middleware (applied in order):

1. **Recovery** — catches panics, returns 500 (disable: `DisableRecovery`)
2. **RequestID** — generates/propagates `X-Request-ID` header
3. **RequestLog** — logs method, path, status, duration (disable: `DisableRequestLog`)
4. **CORS** — Cross-Origin Resource Sharing (enable: set `CORSOrigins`)

Custom middleware is appended after built-ins via `Config.Middleware`.

### Exported middleware

```go
mcpserver.Recovery(logger)        // panic recovery
mcpserver.RequestID()             // X-Request-ID generation
mcpserver.RequestLog(logger)      // request logging
mcpserver.CORS(mcpserver.CORSConfig{...}) // CORS
mcpserver.Chain(handler, mw...)   // apply middleware chain
mcpserver.RequestIDFromContext(ctx) // retrieve request ID
```

## Examples

### External context (no double signal handler)

```go
ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
defer cancel()

// ... init DB, caches using ctx ...

mcpserver.Run(server, mcpserver.Config{
	Name:    "my-service",
	Version: version,
	Context: ctx, // reuse parent context
})
```

### CORS with Max-Age

```go
mcpserver.Run(server, mcpserver.Config{
	Name:        "my-api",
	Version:     version,
	CORSOrigins: []string{"https://app.example.com"},
	CORSMaxAge:  3600, // cache preflight for 1 hour
})
```

### Readiness check with DB ping

```go
mcpserver.Run(server, mcpserver.Config{
	Name:    "my-service",
	Version: version,
	ReadinessCheck: func() error {
		return db.Ping(context.Background())
	},
})
```

### Custom metrics and extra routes

```go
mcpserver.Run(server, mcpserver.Config{
	Name:         "go-wp",
	Version:      version,
	Port:         "8894",
	WriteTimeout: 600 * time.Second,
	Metrics:      engine.FormatMetrics,
	Routes: func(mux *http.ServeMux) {
		mux.HandleFunc("POST /cache/clear", handleCacheClear)
	},
	OnShutdown: func() {
		wpserver.Shutdown()
	},
})
```

### Pre-Run stdio check

```go
if mcpserver.IsStdio() {
	// skip heavy init (DB pools, caches) in stdio mode
}
```

## License

MIT
