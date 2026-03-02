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
