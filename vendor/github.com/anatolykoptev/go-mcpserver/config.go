package mcpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultPort            = "8080"
	defaultReadTimeout     = 30 * time.Second
	defaultWriteTimeout    = 0 // disabled for SSE — tools manage own timeout via context
	defaultShutdownTimeout = 10 * time.Second
	defaultToolTimeout     = 60 * time.Second
	portEnvVar             = "MCP_PORT"
)

// Config controls how the MCP server runs.
type Config struct {
	Name    string // service name for /health + logs (required)
	Version string // version for /health + logs (required)
	Port    string // HTTP port; empty → MCP_PORT env → "8080"

	WriteTimeout    time.Duration // default 0 (disabled for SSE compat; tools manage own timeout)
	ReadTimeout     time.Duration // default 30s
	ShutdownTimeout time.Duration // default 10s

	Metrics func() string        // if set, registers GET /metrics
	Routes  func(*http.ServeMux) // extra routes after /mcp, /health, /metrics

	Middleware       []Middleware // custom middleware, applied after built-ins
	CORSOrigins      []string     // nil = no CORS; ["*"] = allow all
	CORSMaxAge       int          // preflight Max-Age in seconds; 0 = omit header
	CORSAllowHeaders []string     // nil = default (Content-Type, Authorization, X-Request-ID)
	ReadinessCheck   func() error // nil = /health/ready always returns 200

	DisableRecovery   bool  // default false (recovery ON)
	DisableHealth     bool  // set true to register custom /health in Routes
	DisableRequestLog bool  // default false (request logging ON)
	DisableMCP        bool  // skip /mcp route registration; server param may be nil
	Stateless         *bool // nil = default true; *false = stateful (session) mode

	MCPReceivingMiddleware []mcp.Middleware // applied to incoming JSON-RPC (client→server)
	MCPSendingMiddleware   []mcp.Middleware // applied to outgoing JSON-RPC (server→client)

	ToolTimeout  time.Duration            // default tool execution timeout; 0 = 60s; tools can override via ToolTimeouts
	ToolTimeouts map[string]time.Duration // per-tool timeout overrides; key = tool name

	SessionTimeout time.Duration  // idle session timeout; 0 = never (passed to StreamableHTTPOptions)
	EventStore     mcp.EventStore // stream resumption; nil = MemoryEventStore (auto-enabled)
	JSONResponse   bool           // true = application/json instead of text/event-stream
	MCPLogger      *slog.Logger   // separate logger for StreamableHTTP handler; nil = none

	BearerAuth *BearerAuth // nil = no auth; wraps /mcp only (see auth.go)
	RESTBridge bool   // enable /api/tools/* REST endpoints (auto-generated from MCP tools)
	RESTPrefix string // URL prefix for REST endpoints; default "/api"

	Context    context.Context // nil → internal signal.NotifyContext(SIGINT, SIGTERM)
	Logger     *slog.Logger    // nil → auto (stdout HTTP / stderr stdio, LevelInfo)
	OnShutdown func()          // called before HTTP shutdown
}

func validate(cfg Config) error {
	if cfg.Name == "" {
		return errors.New("mcpserver: Config.Name is required")
	}
	if cfg.Version == "" {
		return errors.New("mcpserver: Config.Version is required")
	}
	return nil
}

func withDefaults(cfg Config) Config {
	if cfg.Port == "" {
		if p := os.Getenv(portEnvVar); p != "" {
			cfg.Port = p
		} else {
			cfg.Port = defaultPort
		}
	}
	if cfg.ReadTimeout == 0 {
		cfg.ReadTimeout = defaultReadTimeout
	}
	if cfg.WriteTimeout == 0 {
		cfg.WriteTimeout = defaultWriteTimeout
	}
	if cfg.ShutdownTimeout == 0 {
		cfg.ShutdownTimeout = defaultShutdownTimeout
	}
	if cfg.ToolTimeout == 0 {
		cfg.ToolTimeout = defaultToolTimeout
	}
	// Enable stream resumption by default — prevents lost events after reconnect.
	if cfg.EventStore == nil {
		cfg.EventStore = mcp.NewMemoryEventStore(nil)
	}
	return cfg
}

func applyMCPMiddleware(server *mcp.Server, cfg Config) {
	if server == nil {
		return
	}
	// Tool timeout middleware — always first so it wraps everything.
	server.AddReceivingMiddleware(ToolTimeoutMiddleware(cfg))

	if cfg.BearerAuth != nil && cfg.BearerAuth.ToolFilter != nil {
		server.AddReceivingMiddleware(toolFilterMiddleware(cfg.BearerAuth.ToolFilter))
	}
	if len(cfg.MCPReceivingMiddleware) > 0 {
		server.AddReceivingMiddleware(cfg.MCPReceivingMiddleware...)
	}
	if len(cfg.MCPSendingMiddleware) > 0 {
		server.AddSendingMiddleware(cfg.MCPSendingMiddleware...)
	}
}

func buildMiddleware(cfg Config, logger *slog.Logger) []Middleware {
	var mws []Middleware
	if !cfg.DisableRecovery {
		mws = append(mws, Recovery(logger))
	}
	mws = append(mws, RequestID())
	if !cfg.DisableRequestLog {
		mws = append(mws, RequestLog(logger))
	}
	if len(cfg.CORSOrigins) > 0 {
		mws = append(mws, CORS(CORSConfig{
			Origins:      cfg.CORSOrigins,
			MaxAge:       cfg.CORSMaxAge,
			AllowHeaders: cfg.CORSAllowHeaders,
		}))
	}
	mws = append(mws, cfg.Middleware...)
	return mws
}
