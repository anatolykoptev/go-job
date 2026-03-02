package mcpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"
)

const (
	defaultPort            = "8080"
	defaultReadTimeout     = 30 * time.Second
	defaultWriteTimeout    = 120 * time.Second
	defaultShutdownTimeout = 10 * time.Second
	portEnvVar             = "MCP_PORT"
)

// Config controls how the MCP server runs.
type Config struct {
	Name    string // service name for /health + logs (required)
	Version string // version for /health + logs (required)
	Port    string // HTTP port; empty → MCP_PORT env → "8080"

	WriteTimeout    time.Duration // default 120s
	ReadTimeout     time.Duration // default 30s
	ShutdownTimeout time.Duration // default 10s

	Metrics func() string        // if set, registers GET /metrics
	Routes  func(*http.ServeMux) // extra routes after /mcp, /health, /metrics

	Middleware       []Middleware  // custom middleware, applied after built-ins
	CORSOrigins      []string     // nil = no CORS; ["*"] = allow all
	CORSMaxAge       int          // preflight Max-Age in seconds; 0 = omit header
	CORSAllowHeaders []string     // nil = default (Content-Type, Authorization, X-Request-ID)
	ReadinessCheck   func() error // nil = /health/ready always returns 200

	DisableRecovery   bool // default false (recovery ON)
	DisableHealth     bool // set true to register custom /health in Routes
	DisableRequestLog bool // default false (request logging ON)

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
	return cfg
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
