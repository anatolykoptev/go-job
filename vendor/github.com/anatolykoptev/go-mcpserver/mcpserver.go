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

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

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

// Run starts the MCP server and blocks until a signal is received.
// In stdio mode (--stdio flag), it runs via stdin/stdout.
// Otherwise, it starts an HTTP server with middleware, /mcp, /health, and optional /metrics.
func Run(server *mcp.Server, cfg Config) error {
	if err := validate(cfg); err != nil {
		return err
	}
	if !cfg.DisableMCP && server == nil {
		return errors.New("mcpserver: server must not be nil when DisableMCP is false")
	}
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
		ctx := cfg.Context
		if ctx == nil {
			ctx = context.Background()
		}
		return server.Run(ctx, &mcp.StdioTransport{})
	}

	var (
		sigCtx context.Context
		cancel context.CancelFunc
	)
	if cfg.Context != nil {
		sigCtx, cancel = context.WithCancel(cfg.Context)
	} else {
		sigCtx, cancel = signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	}
	defer cancel()

	h := buildHandler(server, cfg, logger)

	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      h,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
	}

	listenErr := make(chan error, 1)
	go func() {
		logger.Info("listening",
			slog.String("service", cfg.Name),
			slog.String("addr", srv.Addr),
		)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			listenErr <- err
		}
	}()

	select {
	case <-sigCtx.Done():
		// normal shutdown path
	case err := <-listenErr:
		logger.Error("server failed", slog.Any("error", err))
		cancel()
		return err
	}
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

// Build returns an http.Handler with middleware, health, and optional /mcp routes.
// Use for testing or embedding in a custom server; use [Run] for production.
func Build(server *mcp.Server, cfg Config) (http.Handler, error) {
	if err := validate(cfg); err != nil {
		return nil, err
	}
	if !cfg.DisableMCP && server == nil {
		return nil, errors.New("mcpserver: server must not be nil when DisableMCP is false")
	}
	cfg = withDefaults(cfg)
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}
	return buildHandler(server, cfg, logger), nil
}

func buildHandler(server *mcp.Server, cfg Config, logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()

	if !cfg.DisableMCP {
		stateless := cfg.Stateless == nil || *cfg.Stateless
		handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
			return server
		}, &mcp.StreamableHTTPOptions{Stateless: stateless})
		mux.Handle("/mcp", handler)
		mux.Handle("/mcp/", handler)
	}

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

	return Chain(mux, buildMiddleware(cfg, logger)...)
}
