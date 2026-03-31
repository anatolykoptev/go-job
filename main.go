// go_job — Job, Remote & Freelance Search MCP server.
//
// Exposes MCP tools for job search, remote work, freelance, resume, interview prep, and more.
// Runs as HTTP MCP server or stdio transport.
package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/anatolykoptev/go-kit/env"
	"github.com/anatolykoptev/go-mcpserver"
	"github.com/anatolykoptev/go-stealth/proxypool"
	twitter "github.com/anatolykoptev/go-twitter"
	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/anatolykoptev/go_job/internal/jobserver"
	"github.com/anatolykoptev/go_job/internal/social"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var (
	version = "dev"
	mcpPort = env.Str("MCP_PORT", "8891")
)

func main() {
	initEngine()

	slog.Info("starting go_job",
		slog.String("port", mcpPort),
	)

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "go_job",
		Version: version,
	}, nil)

	jobserver.RegisterTools(server)
	slog.Info("tools registered", slog.Int("count", 30))

	hooks := mcpserver.MCPHooks{
		OnToolCall: func(_ context.Context, _ string) {
			engine.IncrToolCall()
		},
		OnToolResult: func(_ context.Context, name string, dur time.Duration, isErr bool) {
			slog.Info("tool_result", slog.String("tool", name), slog.Duration("duration", dur), slog.Bool("error", isErr))
		},
	}

	if err := mcpserver.Run(server, mcpserver.Config{
		Name:                   "go_job",
		Version:                version,
		Port:                   mcpPort,
		WriteTimeout:           600 * time.Second,
		SessionTimeout:         10 * time.Minute,
		MCPLogger:              slog.Default(),
		Metrics:                engine.FormatMetrics,
		MCPReceivingMiddleware: []mcp.Middleware{hooks.Middleware()},
	}); err != nil {
		slog.Error("server failed", slog.Any("error", err))
	}
}

func initEngine() {
	c := engine.Config{
		SearxngURL:            env.Str("SEARXNG_URL", ""),
		LLMAPIKey:             env.Str("LLM_API_KEY", ""),
		LLMAPIKeyFallbacks:    env.List("LLM_API_KEY_FALLBACKS", ""),
		LLMAPIBase:            env.Str("LLM_API_BASE", "http://127.0.0.1:8317/v1"),
		LLMModel:              env.Str("LLM_MODEL", "gemini-3.1-flash-lite-preview"),
		LLMTemperature:        env.Float("LLM_TEMPERATURE", 0.1),
		LLMMaxTokens:          env.Int("LLM_MAX_TOKENS", 16384),
		MaxFetchURLs:          env.Int("MAX_FETCH_URLS", 8),
		MaxContentChars:       env.Int("MAX_CONTENT_CHARS", 6000),
		FetchTimeout:          env.Duration("FETCH_TIMEOUT", 10*time.Second),
		GithubToken:           env.Str("GITHUB_TOKEN", ""),
		CacheMaxEntries:       env.Int("CACHE_MAX_ENTRIES", 1000),
		CacheCleanupInterval:  env.Duration("CACHE_CLEANUP_INTERVAL", 300*time.Second),
		IndeedAPIKey:          env.Str("INDEED_API_KEY", ""),
		DatabaseURL:           env.Str("DATABASE_URL", ""),
		MemDBURL:              env.Str("MEMDB_URL", ""),
		MemDBServiceSecret:    env.Str("INTERNAL_SERVICE_SECRET", ""),
		EmbedURL:              env.Str("EMBED_URL", ""),
		BountyHighConfidence:  float32(env.Float("BOUNTY_HIGH_CONF", 0.82)),
		BountyHighConfGap:     float32(env.Float("BOUNTY_HIGH_CONF_GAP", 0.04)),
		BountyHighConfMax:     env.Int("BOUNTY_HIGH_CONF_MAX", 10),
		BountyMedConfMax:      env.Int("BOUNTY_MED_CONF_MAX", 3),
		BountySkillBoost:      float32(env.Float("BOUNTY_SKILL_BOOST", 0.05)),
		BountyMinRelevance:    float32(env.Float("BOUNTY_MIN_RELEVANCE", 0.75)),
		VaelorNotifyURL:       env.Str("VAELOR_NOTIFY_URL", ""),
		BountyNotifyChatID:    env.Str("BOUNTY_NOTIFY_CHAT_ID", "428660"),
		BountyMonitorInterval: env.Duration("BOUNTY_MONITOR_INTERVAL", 15*time.Minute),
		DirectDDG:             env.Bool("DIRECT_DDG", false),
		DirectStartpage:       env.Bool("DIRECT_STARTPAGE", false),
		DirectBrave:           env.Bool("DIRECT_BRAVE", false),
		DirectReddit:          env.Bool("DIRECT_REDDIT", false),
	}

	// Initialize proxy pool from Webshare API (optional).
	if apiKey := os.Getenv("WEBSHARE_API_KEY"); apiKey != "" {
		pool, err := proxypool.NewWebshare(apiKey)
		if err != nil {
			slog.Warn("proxy pool init failed, running without proxy", slog.Any("error", err))
		} else {
			c.ProxyPool = pool
			slog.Info("proxy pool initialized", slog.Int("proxies", pool.Len()))
		}
	}

	// go-social client (optional — centralized account pool)
	if socialURL := env.Str("GO_SOCIAL_URL", ""); socialURL != "" {
		socialToken := env.Str("GO_SOCIAL_TOKEN", "")
		c.SocialClient = social.NewClient(socialURL, socialToken, "go-job")
		slog.Info("go-social client initialized", slog.String("url", socialURL))
	}

	// Twitter client (fallback — local accounts or guest mode)
	accounts := twitter.ParseAccounts(env.Str("TWITTER_ACCOUNTS", ""))
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

	// Embed client (for embedding-based bounty matching)
	if c.EmbedURL != "" {
		jobs.SetEmbedClient(jobs.NewEmbedClient(c.EmbedURL))
		slog.Info("embed client initialized", slog.String("url", c.EmbedURL))
	}

	cacheTTL := env.Duration("CACHE_TTL", 15*time.Minute)
	engine.InitCache(env.Str("REDIS_URL", ""), cacheTTL, c.CacheMaxEntries, c.CacheCleanupInterval)

	// Start background monitors.
	jobs.StartBountyMonitor(context.Background())
	jobs.StartSecurityMonitor(context.Background())
	jobs.StartFreelanceMonitor(context.Background())
}
