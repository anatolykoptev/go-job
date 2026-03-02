// Package mcpserver provides a bootstrap library for Go MCP servers.
//
// It handles stdio detection, slog setup, signal handling, StreamableHTTP handler,
// middleware chain, /health endpoints, and graceful shutdown.
package mcpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
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

	Middleware     []Middleware  // custom middleware, applied after built-ins
	CORSOrigins    []string     // nil = no CORS; ["*"] = allow all
	ReadinessCheck func() error // nil = /health/ready always returns 200

	DisableRecovery   bool // default false (recovery ON)
	DisableHealth     bool // set true to register custom /health in Routes
	DisableRequestLog bool // default false (request logging ON)

	Logger     *slog.Logger // nil → auto (stdout HTTP / stderr stdio, LevelInfo)
	OnShutdown func()       // called before HTTP shutdown
}

// IsStdio reports whether --stdio was passed on the command line.
func IsStdio() bool {
	return isStdio()
}

func isStdio() bool {
	for _, arg := range os.Args[1:] {
		if arg == "--stdio" {
			return true
		}
	}
	return false
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
		mws = append(mws, CORS(cfg.CORSOrigins))
	}
	mws = append(mws, cfg.Middleware...)
	return mws
}

// Run starts the MCP server and blocks until a signal is received.
// In stdio mode (--stdio flag), it runs via stdin/stdout.
// Otherwise, it starts an HTTP server with middleware, /mcp, /health, and optional /metrics.
func Run(server *mcp.Server, cfg Config) error {
	cfg = withDefaults(cfg)
	stdio := isStdio()

	logger := cfg.Logger
	if logger == nil {
		w := os.Stdout
		if stdio {
			w = os.Stderr
		}
		logger = slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}
	slog.SetDefault(logger)

	if stdio {
		logger.Info("running in stdio mode", slog.String("service", cfg.Name))
		return server.Run(context.Background(), &mcp.StdioTransport{})
	}

	sigCtx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, &mcp.StreamableHTTPOptions{
		Stateless: true,
	})

	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)
	mux.Handle("/mcp/", handler)

	registerHealth(mux, cfg)

	if cfg.Metrics != nil {
		mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte(cfg.Metrics()))
		})
	}

	if cfg.Routes != nil {
		cfg.Routes(mux)
	}

	h := Chain(mux, buildMiddleware(cfg, logger)...)

	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      h,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
	}

	go func() {
		logger.Info("listening",
			slog.String("service", cfg.Name),
			slog.String("addr", srv.Addr),
		)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", slog.Any("error", err))
			os.Exit(1) //nolint:gocritic // intentional exit on bind failure
		}
	}()

	<-sigCtx.Done()
	logger.Info("shutting down", slog.String("service", cfg.Name))

	if cfg.OnShutdown != nil {
		cfg.OnShutdown()
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown error", slog.Any("error", err))
		return err
	}

	logger.Info("stopped", slog.String("service", cfg.Name))
	return nil
}
