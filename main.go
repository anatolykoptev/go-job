// go_job — Job, Remote & Freelance Search MCP server.
//
// Exposes three MCP tools: job_search, remote_work_search, freelance_search.
// Runs as HTTP MCP server or stdio transport.
//
// Currently depends on github.com/anatolykoptev/go-search for the engine layer.
// Designed to be gradually decoupled — see internal/engine/ for future migration.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	stealth "github.com/anatolykoptev/go-stealth"
	"github.com/anatolykoptev/go-stealth/proxypool"
	twitter "github.com/anatolykoptev/go-twitter"
	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/anatolykoptev/go_job/internal/jobserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var (
	version = "dev"
	mcpPort = env("MCP_PORT", "8891")
)

func main() {
	stdio := isStdio()

	logWriter := os.Stdout
	if stdio {
		logWriter = os.Stderr
	}
	logger := slog.New(slog.NewTextHandler(logWriter, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	initEngine()

	logger.Info("starting go_job",
		slog.Bool("stdio", stdio),
		slog.String("port", mcpPort),
	)

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "go_job",
		Version: version,
	}, nil)

	jobserver.RegisterTools(server)
	logger.Info("tools registered", slog.Int("count", 22))

	if stdio {
		logger.Info("running in stdio mode")
		if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
			logger.Error("stdio server failed", slog.Any("error", err))
			os.Exit(1)
		}
		return
	}

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, &mcp.StreamableHTTPOptions{
		Stateless: true,
	})

	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)
	mux.Handle("/mcp/", handler)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","service":"go_job","version":"` + version + `"}`))
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(engine.FormatMetrics()))
	})

	srv := &http.Server{
		Addr:         ":" + mcpPort,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 600 * time.Second,
	}

	sigCtx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	go func() {
		logger.Info("listening", slog.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server failed", slog.Any("error", err))
			os.Exit(1)
		}
	}()

	<-sigCtx.Done()
	logger.Info("shutting down")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown failed", slog.Any("error", err))
	}
	logger.Info("stopped")
}

func isStdio() bool {
	for _, arg := range os.Args[1:] {
		if arg == "--stdio" {
			return true
		}
	}
	return false
}

func initEngine() {
	c := engine.Config{
		SearxngURL:           env("SEARXNG_URL", "http://127.0.0.1:8888"),
		LLMAPIKey:            env("LLM_API_KEY", ""),
		LLMAPIKeyFallbacks:   envList("LLM_API_KEY_FALLBACKS", ""),
		LLMAPIBase:           env("LLM_API_BASE", "https://generativelanguage.googleapis.com/v1beta/openai"),
		LLMModel:             env("LLM_MODEL", "gemini-2.5-flash"),
		LLMTemperature:       envFloat("LLM_TEMPERATURE", 0.1),
		LLMMaxTokens:         envInt("LLM_MAX_TOKENS", 16384),
		MaxFetchURLs:         envInt("MAX_FETCH_URLS", 8),
		MaxContentChars:      envInt("MAX_CONTENT_CHARS", 6000),
		FetchTimeout:         envDuration("FETCH_TIMEOUT", 10*time.Second),
		GithubToken:          env("GITHUB_TOKEN", ""),
		CacheMaxEntries:      envInt("CACHE_MAX_ENTRIES", 1000),
		CacheCleanupInterval: envDuration("CACHE_CLEANUP_INTERVAL", 300*time.Second),
		IndeedAPIKey:         env("INDEED_API_KEY", ""),
		DatabaseURL:          env("DATABASE_URL", ""),
		MemDBURL:             env("MEMDB_URL", ""),
		MemDBServiceSecret:   env("INTERNAL_SERVICE_SECRET", ""),
		HTTPClient: &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        20,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     60 * time.Second,
			},
		},
	}
	var opts []stealth.ClientOption
	opts = append(opts, stealth.WithTimeout(15))

	if apiKey := os.Getenv("WEBSHARE_API_KEY"); apiKey != "" {
		pool, err := proxypool.NewWebshare(apiKey)
		if err != nil {
			slog.Warn("proxy pool init failed, running without proxy", slog.Any("error", err))
		} else {
			opts = append(opts, stealth.WithProxyPool(pool))
			slog.Info("proxy pool initialized", slog.Int("proxies", pool.Len()))
		}
	}

	bc, err := stealth.NewClient(opts...)
	if err != nil {
		slog.Error("stealth client init failed", slog.Any("error", err))
	} else {
		c.BrowserClient = bc
		slog.Info("stealth browser client initialized")
	}

	// Twitter client (optional — guest mode if no accounts configured)
	accounts := twitter.ParseAccounts(os.Getenv("TWITTER_ACCOUNTS"))
	openCount := 2
	if len(accounts) > 0 {
		openCount = 0
	}
	tw, err := twitter.NewClient(twitter.ClientConfig{
		Accounts:         accounts,
		OpenAccountCount: openCount,
	})
	if err != nil {
		slog.Warn("twitter client init failed", slog.Any("error", err))
	} else {
		c.TwitterClient = tw
		slog.Info("twitter client ready", slog.Int("pool_size", tw.Pool().Size()))
	}

	engine.Init(c)

	// Resume DB (PostgreSQL + AGE graph)
	if c.DatabaseURL != "" {
		rdb, err := jobs.ConnectResumeDB(context.Background(), c.DatabaseURL)
		if err != nil {
			slog.Warn("resume DB init failed", slog.Any("error", err))
		} else {
			jobs.SetResumeDB(rdb)
			slog.Info("resume DB initialized")
		}
	}

	// MemDB vector client
	if c.MemDBURL != "" && c.MemDBServiceSecret != "" {
		jobs.SetMemDB(jobs.NewMemDBClient(c.MemDBURL, c.MemDBServiceSecret))
		slog.Info("memdb client initialized", slog.String("url", c.MemDBURL))
	}

	cacheTTL := envDuration("CACHE_TTL", 15*time.Minute)
	engine.InitCache(env("REDIS_URL", ""), cacheTTL, c.CacheMaxEntries, c.CacheCleanupInterval)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func envList(key, def string) []string {
	v := env(key, def)
	return strings.Split(v, ",")
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if secs, err := strconv.ParseFloat(v, 64); err == nil {
			return time.Duration(secs * float64(time.Second))
		}
	}
	return def
}
