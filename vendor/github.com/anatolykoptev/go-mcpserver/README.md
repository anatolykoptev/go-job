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
	"github.com/anatolykoptev/go-mcpserver"
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
- **Health endpoint** — `GET /health` returning `{"status":"ok","service":"...","version":"..."}`
- **Recovery middleware** — catches panics, returns 500
- **Configurable timeouts** — read (30s), write (120s), shutdown (10s)

## Config

```go
type Config struct {
	Name    string            // service name (required)
	Version string            // version string (required)
	Port    string            // HTTP port; empty → MCP_PORT env → "8080"

	WriteTimeout    time.Duration // default 120s
	ReadTimeout     time.Duration // default 30s
	ShutdownTimeout time.Duration // default 10s

	Metrics  func() string        // if set, registers GET /metrics
	Routes   func(*http.ServeMux) // extra routes after /mcp, /health, /metrics

	DisableRecovery bool         // default false (recovery ON)
	DisableHealth   bool         // set true to register custom /health in Routes

	Logger     *slog.Logger     // nil → auto
	OnShutdown func()           // called before HTTP shutdown
}
```

## Examples

### Custom metrics and extra routes

```go
mcpserver.Run(server, mcpserver.Config{
	Name:    "go-wp",
	Version: version,
	Port:    "8894",
	WriteTimeout: 600 * time.Second,
	Metrics: engine.FormatMetrics,
	Routes: func(mux *http.ServeMux) {
		mux.HandleFunc("POST /cache/clear", handleCacheClear)
	},
	OnShutdown: func() {
		wpserver.Shutdown()
	},
})
```

### Custom health endpoint

```go
mcpserver.Run(server, mcpserver.Config{
	Name:         "go-hully",
	Version:      version,
	DisableHealth: true,
	Routes: func(mux *http.ServeMux) {
		mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"status":"ok","db":%v}`, db.Ping() == nil)
		})
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
