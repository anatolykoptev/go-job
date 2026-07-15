// go_job — Job, Remote & Freelance Search MCP server.
//
// Exposes MCP tools for job search, remote work, freelance, resume, interview prep, and more.
// Runs as HTTP MCP server or stdio transport.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/anatolykoptev/go-kit/env"
	kitembed "github.com/anatolykoptev/go-kit/embed"
	kitmetrics "github.com/anatolykoptev/go-kit/metrics"
	"github.com/anatolykoptev/go-kit/metrics/mcpmw"
	linkedin "github.com/anatolykoptev/go-linkedin"
	"github.com/anatolykoptev/go-mcpserver"
	"github.com/anatolykoptev/go-stealth/proxypool"
	twitter "github.com/anatolykoptev/go-twitter"
	"github.com/anatolykoptev/go-twitter/social"
	"github.com/anatolykoptev/go_job/internal/adminui"
	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/anatolykoptev/go_job/internal/engine/jobs/applications"
	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/anatolykoptev/go_job/internal/pdfrender"
	"github.com/anatolykoptev/go_job/internal/hunt/discovery"
	"github.com/anatolykoptev/go_job/internal/hunt/enrich"
	"github.com/anatolykoptev/go_job/internal/hunt/notify"
	"github.com/anatolykoptev/go_job/internal/huntworker"
	"github.com/anatolykoptev/go_job/internal/jobserver"
	"github.com/anatolykoptev/go_job/internal/oversize"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var (
	version          = "dev"
	mcpPort          = env.Str("MCP_PORT", "8891")
	fetchDirectFirst = env.Str("FETCH_DIRECT_FIRST", "auto")
)

func main() {
	huntNotifier := initEngine()

	slog.Info("starting go_job",
		slog.String("port", mcpPort),
	)

	sigCtx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Start periodic Telegram bot token health check (noop when notifier is nil
	// or not a *notify.ProductNotifier). Validates the token every hour via GetMe
	// and sets the gojob_hunt_notify_health gauge (1=healthy, 0=revoked).
	// Alert: gojob_hunt_notify_health == 0 for >5m.
	if n, ok := huntNotifier.(*notify.ProductNotifier); ok {
		go startNotifyHealthCheck(sigCtx, n)
	}

	// Start durable ATS ingest worker (noop when HUNT_INGEST_ENABLED is false or
	// the hunt store is unavailable).  Must run after initEngine wired the store.
	// huntNotifier is the same Telegram notifier wired to the store so the worker
	// fires on OutcomeCreated without going back through the store's unexported field.
	huntworker.StartWorker(sigCtx, engine.GetHuntStore(), huntNotifier)
	huntworker.StartOpportunityWorker(sigCtx, engine.GetHuntStore())

	startPrometheusScrape(sigCtx, slog.Default())

	// Compose the application PDF authority.
	// TypstAdapter wraps pandoc+typst; gracefully degrades when binaries absent.
	adapter := pdfrender.New()
	legacyDir := env.Str("APPLICATIONS_DIR", "/data/applications")
	authority := applications.New(adapter, legacyDir)
	// Probe binary availability at startup: sets gojob_pdf_renderer_available
	// gauge (1=present, 0=absent) so post-deploy verification is unambiguous.
	if !adapter.Ready() {
		slog.Warn("PDF renderer binaries absent — application_persist will degrade to md-only")
	}

	// Operator admin UI (go-panel) on :8896 — fail-soft (no-op without ADMIN_* env).
	if hs := engine.GetHuntStore(); hs != nil {
		startAdminServer(sigCtx, hs, authority, slog.Default())
	}

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "go_job",
		Version: version,
	}, nil)

	jobserver.RegisterTools(server, authority)
	slog.Info("tools registered", slog.Int("count", 44))

	hooks := mcpserver.MCPHooks{
		OnToolCall: func(_ context.Context, _ string) {
			engine.IncrToolCall()
		},
		OnToolResult: func(_ context.Context, name string, dur time.Duration, isErr bool) {
			slog.Info("tool_result", slog.String("tool", name), slog.Duration("duration", dur), slog.Bool("error", isErr))
		},
	}

	if err := mcpserver.Run(server, mcpserver.Config{
		Name:    "go_job",
		Version: version,
		Port:    mcpPort,
		// Return tool results as a single application/json body instead of the
		// go-sdk default text/event-stream framing. The SSE path puts the entire
		// JSON result on ONE `data:` line; large results (e.g. resume_profile's
		// ~17KB) exceed the SSE single-line buffer limit on the WAN MCP client and
		// the connection is severed after the 54-byte event prefix → "transport
		// dropped; response lost". go-job's tools are all unary request/response
		// (no mid-call progress notifications), so SSE buys nothing here. A plain
		// JSON body has no per-line limit and is delivered intact. Clients send
		// `Accept: application/json, text/event-stream`, so this is fully negotiated.
		JSONResponse: true,
		// go-mcpserver v0.15.0+ plumbs http.Server.IdleTimeout (default 5m), which
		// keeps idle pooled connections alive across pauses between tool calls — so
		// the first MCP call after an idle window no longer drops. No ReadTimeout
		// override needed (it defaults to 30s, the correct header-read deadline).
		WriteTimeout:   600 * time.Second,
		SessionTimeout: 10 * time.Minute,
		// ToolTimeout is the per-tool execution deadline enforced by
		// ToolTimeoutMiddleware. The 90s default is fine for cheap DB/parse tools
		// but too tight for tools that fan out web research and/or chain multiple
		// LLM calls — those legitimately run minutes. heavyToolTimeouts raises the
		// budget for that class; everything else keeps the 90s default.
		ToolTimeouts:           heavyToolTimeouts(),
		MCPLogger:              slog.Default(),
		Metrics:                engine.FormatMetrics,
		MCPReceivingMiddleware: []mcp.Middleware{hooks.Middleware(), mcpmw.Middleware(engine.Reg(), "tool")},
	}); err != nil {
		slog.Error("server failed", slog.Any("error", err))
	}
}

// heavyToolTimeouts returns per-tool execution-timeout overrides for tools that
// fan out web research and/or chain multiple LLM calls and therefore legitimately
// run well past the 90s ToolTimeout default. Two tiers:
//
//   - web-research tools (multiple SearXNG fetches + LLM synthesis): 8m
//   - multi-LLM-call tools (2+ sequential LLM calls, no live web fetch): 5m
//
// The two budgets are env-overridable (HEAVY_TOOL_TIMEOUT / MULTI_LLM_TIMEOUT)
// so ops can tune without a rebuild. Tools NOT listed here keep the 90s default.
//
// This is the server-side companion to the request-path bound on the optional
// company-research substep (jobs.ResearchCompanyBounded): the substep bound stops
// one slow dependency from dominating a tool, while these budgets give the tool
// as a whole enough headroom for its inherent LLM/IO cost.
func heavyToolTimeouts() map[string]time.Duration {
	web := env.Duration("HEAVY_TOOL_TIMEOUT", 8*time.Minute)
	multiLLM := env.Duration("MULTI_LLM_TIMEOUT", 5*time.Minute)

	out := make(map[string]time.Duration)
	// Web-research tools: live SearXNG fetches + LLM synthesis.
	for _, t := range []string{
		"research",         // 3 parallel searches + 2 LLM calls
		"application_prep", // analysis + cover letter + interview prep + company research, in parallel
		"interview_prep",   // company/person research + LLM
		"pitch_generate",   // research + LLM
		"resume_generate",  // JD extract + optional company research + assemble (up to 3 LLM + 3 fetch)
		"opportunity_analyze",
	} {
		out[t] = web
	}
	// Multi-LLM tools: 2+ sequential LLM calls, no live web fetch.
	for _, t := range []string{
		"master_resume_build", // 2 LLM calls over a large corpus
		"resume_enrich",       // 2 LLM calls
		"resume_tailor",       // LLM assemble
		"cover_letter_generate",
		"resume_analyze",
		"negotiation_prep",
		"offer_compare",
		"project_showcase",
		"skill_gap",
		"job_match_score",
	} {
		out[t] = multiLLM
	}
	// job_search fans out to up to 17 parallel connectors, each with its own
	// network I/O and optional LLM post-processing. The 90s default ToolTimeout
	// is too tight: the HN connector alone can spend up to 30s on Firebase fan-out
	// (hnFanoutBudget) after thread-find + Algolia, and when platform=all all
	// connectors run concurrently with platform-level retry. 3m gives ample
	// headroom for the worst-case parallel fan-out without the tool being
	// classified as a "heavy LLM" tool.
	out["job_search"] = env.Duration("JOB_SEARCH_TIMEOUT", 3*time.Minute)
	return out
}

// startPrometheusScrape runs an HTTP server exposing /metrics on PROM_PORT
// (default 9891 = MCP_PORT+1000) for prometheus scrape. Separate port avoids
// BearerAuth on scrape traffic; bound to all interfaces for container scrape.
func startPrometheusScrape(ctx context.Context, logger *slog.Logger) {
	promPort := env.Str("PROM_PORT", "9891")
	mux := http.NewServeMux()
	mux.Handle("/metrics", kitmetrics.MetricsHandler())
	srv := &http.Server{
		Addr:              ":" + promPort,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		logger.Info("prometheus scrape endpoint", slog.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("prom endpoint", slog.Any("error", err))
		}
	}()
	go func() { //nolint:gosec // G118: intentional — request ctx is done, shutdown needs a fresh context
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()
}

// initEngine initialises the global engine (DB, proxy pool, clients) and returns
// the hunt.Notifier that was wired to the hunt store (nil if bot init failed or
// the store was not configured). The caller (main) passes this to StartWorker so
// the ingest worker can fire Telegram notifications on OutcomeCreated.
func initEngine() hunt.Notifier {
	directFirst, initPool := resolveFetchMode(fetchDirectFirst)

	c := engine.Config{
		SearxngURL:           env.Str("SEARXNG_URL", ""),
		LLMAPIKey:            env.Str("LLM_API_KEY", ""),
		LLMAPIKeyFallbacks:   env.List("LLM_API_KEY_FALLBACKS", ""),
		LLMAPIBase:           env.Str("LLM_API_BASE", "http://127.0.0.1:8317/v1"),
		LLMModel:             env.Str("LLM_MODEL", "gemini-3.1-flash-lite-preview"),
		LLMModelFallback:     env.Str("LLM_MODEL_FALLBACK", ""),
		LLMTemperature:       env.Float("LLM_TEMPERATURE", 0.1),
		LLMMaxTokens:         env.Int("LLM_MAX_TOKENS", 16384),
		MaxFetchURLs:         env.Int("MAX_FETCH_URLS", 8),
		MaxContentChars:      env.Int("MAX_CONTENT_CHARS", 6000),
		FetchTimeout:         env.Duration("FETCH_TIMEOUT", 10*time.Second),
		GithubToken:          env.Str("GITHUB_TOKEN", ""),
		CacheMaxEntries:      env.Int("CACHE_MAX_ENTRIES", 1000),
		CacheCleanupInterval: env.Duration("CACHE_CLEANUP_INTERVAL", 300*time.Second),
		IndeedAPIKey:         env.Str("INDEED_API_KEY", ""),
		DatabaseURL:          env.Str("DATABASE_URL", ""),
		EmbedURL:             env.Str("EMBED_URL", ""),
		OxBrowserURL:         env.Str("OX_BROWSER_URL", ""),
		// VaelorNotifyURL and BountyNotifyChatID removed — notifications now go via
		// the go-kit ProductSink bot (TELEGRAM_BOT_TOKEN + HUNT_NOTIFY_CHAT_ID).
		DirectDDG:              env.Bool("DIRECT_DDG", false),
		DirectStartpage:        env.Bool("DIRECT_STARTPAGE", false),
		DirectBrave:            env.Bool("DIRECT_BRAVE", false),
		DirectReddit:           env.Bool("DIRECT_REDDIT", false),
		DirectWikipedia:        env.Bool("DIRECT_WIKIPEDIA", false),
		DirectMarginalia:       env.Bool("DIRECT_MARGINALIA", false),
		SearchEarlyReturnAt:    env.Int("SEARCH_EARLY_RETURN_AT", 0),
		SearchPerSourceTimeout: env.Duration("SEARCH_PER_SOURCE_TIMEOUT", 0),
		FetchDirectFirst:       directFirst,
	}

	// Initialize proxy pool from Webshare API (optional).
	// Skipped when FETCH_DIRECT_FIRST=direct (direct-only) or FETCH_DIRECT_FIRST=off.
	if initPool {
		if apiKey := env.Str("WEBSHARE_API_KEY", ""); apiKey != "" {
			pool, err := proxypool.NewWebshare(apiKey)
			if err != nil {
				slog.Warn("proxy pool init failed, running without proxy", slog.Any("error", err))
			} else {
				c.ProxyPool = pool
				slog.Info("proxy pool initialized", slog.Int("proxies", pool.Len()))
			}
		}
	}

	// go-social client (optional — centralized account pool)
	if socialURL := env.Str("GO_SOCIAL_URL", ""); socialURL != "" {
		socialToken := env.Str("GO_SOCIAL_TOKEN", "")
		c.SocialClient = social.NewClient(socialURL, socialToken, "go-job")
		slog.Info("go-social client initialized", slog.String("url", socialURL))
	}

	// LinkedIn client via go-social
	if c.SocialClient != nil {
		liCreds, liErr := c.SocialClient.AcquireAccount(context.Background(), "linkedin")
		if liErr == nil {
			liClient, liInitErr := linkedin.New(linkedin.ClientConfig{
				Cookies: liCreds.Credentials,
				Proxy:   liCreds.Proxy,
			})
			if liInitErr == nil {
				c.LinkedInClient = liClient
				slog.Info("linkedin client initialized via go-social")
			} else {
				slog.Warn("linkedin client init failed", slog.Any("error", liInitErr))
			}
		} else {
			slog.Info("no linkedin account in go-social, linkedin tools disabled")
		}
	}

	// Twitter client (fallback — local accounts or guest mode)
	accounts := twitter.ParseAccounts(env.Str("TWITTER_ACCOUNTS", ""))
	openCount := 2
	if len(accounts) > 0 {
		openCount = 0
	}
	// When go-social is configured it owns all Twitter search; the local client
	// is never used for search in that path. Building it with the default
	// OpenAccountCount=2 triggers two guest-token bootstraps at startup that
	// always fail from a datacenter IP (Bad guest token, code 239 / 403).
	// Suppress the noise: build the local client in silent-fallback mode.
	disableGuestFallback := false
	if c.SocialClient != nil {
		openCount = 0
		disableGuestFallback = true
	}
	tw, err := twitter.NewClient(twitter.ClientConfig{
		Accounts:             accounts,
		OpenAccountCount:     openCount,
		DisableGuestFallback: disableGuestFallback,
	})
	if err != nil {
		slog.Warn("twitter client init failed", slog.Any("error", err))
	} else {
		c.TwitterClient = tw
		slog.Info("twitter client ready", slog.Int("pool_size", tw.Pool().Size()))
	}

	engine.Init(c)

	// huntNotifier is set when a valid Telegram bot is configured; nil otherwise.
	var huntNotifier hunt.Notifier

	// Resume DB (PostgreSQL + AGE graph)
	if c.DatabaseURL == "" {
		slog.Warn("hunt persist DISABLED", slog.String("reason", "DATABASE_URL unset"))
		engine.SetHuntPersistEnabled(false)
	}
	if c.DatabaseURL != "" {
		rdb, err := jobs.ConnectResumeDB(context.Background(), c.DatabaseURL)
		if err != nil {
			slog.Warn("resume DB init failed", slog.Any("error", err))
			engine.SetHuntPersistEnabled(false)
		} else {
			jobs.SetResumeDB(rdb)
			slog.Info("resume DB initialized")

			// Wire oversize store on the same pool (fails-soft: optional spill feature).
			osStore := oversize.NewStore(rdb.Pool())
			if err := osStore.Migrate(context.Background()); err != nil {
				slog.Error("oversize migrate failed", slog.Any("error", err))
				// Non-fatal: oversize spill is optional; continue startup.
			} else {
				engine.SetOversizeStore(osStore)
				slog.Info("oversize store ready")
			}

			// Wire hunt store on the same pool.
			// FATAL: when DATABASE_URL is set, hunt persistence is a core dependency —
			// without it, hunt_list/hunt_match MCP tools are broken and jobs are
			// silently ingested without persistence (data loss). Fail-fast rather
			// than degrade silently. When DATABASE_URL is unset, hunt is optional
			// and the graceful disable at line 334 is correct.
			hStore := hunt.NewStore(rdb.Pool())
			if err := hStore.Migrate(context.Background()); err != nil {
				slog.Error("hunt migrate failed — exiting (DATABASE_URL is set, hunt persistence is required)", slog.Any("error", err))
				os.Exit(1)
			} else {
				engine.SetHuntStore(hStore)
				engine.SetHuntPersistEnabled(true)

				// Wire status enrichment (lazy on-read GitHub check) and Telegram notify.
				// Enricher: adapter wraps existing fetchIssueInfoBatch for testability.
				hStore.SetEnricher(enrich.NewEnricher(jobs.NewGithubFetcherAdapter()))
				// Notifier: fires on OutcomeCreated (open-only) for any ingest path.
				// Uses go-kit ProductSink (own bot) — not vaelor loopback.
				// Requires TELEGRAM_BOT_TOKEN + HUNT_NOTIFY_CHAT_ID at deploy.
				// OnSend bridges sent/failed counts into gojob_hunt_notify_total{outcome}.
				notif, notifErr := notify.NewFromEnv(engine.Reg())
				if notifErr != nil {
					slog.Warn("hunt notify: disabled (bot init failed)", slog.Any("error", notifErr))
				} else {
					notif.OnSend = engine.IncrHuntNotify
					hStore.SetNotifier(notif)
					// Capture for the ingest worker — same notifier instance,
					// wired to the worker via StartWorker so the worker fires
					// notifications in runCycle rather than inside UpsertJob.
					huntNotifier = notif
					// Start periodic Telegram bot token health check.
					// Validates the token every hour via GetMe and sets the
					// gojob_hunt_notify_health gauge (1=healthy, 0=revoked).
					// Alert: gojob_hunt_notify_health == 0 for >5m.
					engine.SetHuntNotifyHealth(true) // optimistic at startup (GetMe passed in NewBotAPI)
				}

				slog.Info("hunt store ready")
			}
		}
	}

	// Wire ATS discovery client + raw web searcher (both delegate to go-search's
	// fused multi-source pipeline — Brave-API + ox-browser-search + DDG, ADR-002).
	// GO_SEARCH_URL empty → both stay nil → local SearchDirect fallback.
	// The same Client instance serves both roles: DiscoverBoardURLs (ATS host
	// filtered) for discovery, RawSearch (unfiltered) for person/salary research.
	if goSearchURL := env.Str("GO_SEARCH_URL", ""); goSearchURL != "" {
		searchClient := discovery.NewClient(goSearchURL)
		jobs.SetATSDiscoverer(searchClient)
		jobs.SetRawSearcher(searchClient)
		slog.Info("go-search client wired (ATS discovery + raw web search)",
			slog.String("go_search_url", goSearchURL),
		)
	} else {
		slog.Info("go-search: GO_SEARCH_URL unset — using local SearchDirect fallback for discovery and research")
	}

	// Embed client (go-kit Embedder; auto-resolves EMBED_TOKEN from env).
	if c.EmbedURL != "" {
		embedder, embedErr := kitembed.NewClient(c.EmbedURL,
			kitembed.WithBackend("http"),
			kitembed.WithDim(1024),
			kitembed.WithLogger(slog.Default()),
		)
		if embedErr != nil {
			slog.Error("embed client init failed", slog.Any("error", embedErr))
		} else {
			jobs.SetEmbedClient(embedder)
			slog.Info("embed client initialized", slog.String("url", c.EmbedURL))
		}
	}

	cacheTTL := env.Duration("CACHE_TTL", 15*time.Minute)
	engine.InitCache(env.Str("REDIS_URL", ""), cacheTTL, c.CacheMaxEntries, c.CacheCleanupInterval)

	if env.Bool("SLUG_CACHE_ENABLED", true) {
		redisURL := env.Str("REDIS_URL", "")
		sc := jobs.NewSlugCache(redisURL)
		jobs.SetSlugCache(sc)
		slog.Info("slug cache initialized",
			slog.Bool("redis_l2", redisURL != ""),
		)
	}

	// Background monitors replaced by lazy on-read enrichment (Phase 3).
	// Telegram notify is now wired directly into the ingest hook (store.UpsertX)
	// so it fires on any ingest path — not just from the old monitor goroutines.
	return huntNotifier
}

// resolveFetchMode maps FETCH_DIRECT_FIRST env value to (directFirst, initPool) flags.
// Unknown values fall back to legacy proxy-first + pool init with a warning.
//
//   - "auto"   — direct-first with proxy fallback on anti-bot signals (default).
//   - "direct" — direct-only, no proxy pool initialized (Webshare disabled entirely).
//   - "proxy"  — legacy proxy-first behavior (regression rollback).
//   - "off"    — disable Webshare pool; direct-only without anti-bot fallback.
func resolveFetchMode(s string) (directFirst, initPool bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "auto":
		return true, true
	case "direct":
		return true, false
	case "proxy":
		return false, true
	case "off":
		return false, false
	default:
		slog.Warn("unknown FETCH_DIRECT_FIRST value, falling back to 'proxy'", slog.String("value", s))
		return false, true
	}
}

// startNotifyHealthCheck runs a periodic Telegram bot token health check.
// Calls HealthCheck (GetMe) every hour and updates the gojob_hunt_notify_health
// gauge. If the token is revoked or unreachable, the gauge drops to 0 and the
// alert fires. The goroutine exits when ctx is cancelled (SIGINT/SIGTERM).
func startNotifyHealthCheck(ctx context.Context, n *notify.ProductNotifier) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := n.HealthCheck(checkCtx)
			cancel()
			if err != nil {
				slog.Warn("hunt notify: health check failed", slog.Any("error", err))
				engine.SetHuntNotifyHealth(false)
			} else {
				engine.SetHuntNotifyHealth(true)
			}
		}
	}
}

// startAdminServer starts the operator admin UI (go-panel) on ADMIN_PORT
// (default 8896, host-restricted to 127.0.0.1 by compose), mounted at /admin. Fail-soft: when admin
// credentials are unset (adminui.New returns ok=false) the listener is skipped,
// so deploying before the env is wired changes nothing.
func startAdminServer(ctx context.Context, store *hunt.Store, authority *applications.Authority, logger *slog.Logger) {
	handler, ok := adminui.New(store, authority)
	if !ok {
		logger.Info("admin UI disabled (set ADMIN_HMAC_KEY + ADMIN_PASSWORD to enable)")
		return
	}
	// Bind all interfaces inside the container; host exposure is restricted to
	// 127.0.0.1 by the compose port mapping (127.0.0.1:8896:8896), matching the
	// MCP/prometheus listeners. Binding 127.0.0.1 here would be unreachable via
	// the published port (Docker forwards to the container eth0, not loopback).
	addr := ":" + env.Str("ADMIN_PORT", "8896")
	mux := http.NewServeMux()
	mux.Handle("/admin/", handler)
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		logger.Info("admin UI endpoint", slog.String("addr", addr))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("admin endpoint", slog.Any("error", err))
		}
	}()
	go func() { //nolint:gosec // G118: shutdown needs a fresh context after ctx is done
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()
}
