// go_job — Job, Remote & Freelance Search MCP server.
//
// Exposes MCP tools for job search, remote work, freelance, resume, interview prep, and more.
// Runs as HTTP MCP server or stdio transport.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	kitembed "github.com/anatolykoptev/go-kit/embed"
	"github.com/anatolykoptev/go-kit/env"
	kitmetrics "github.com/anatolykoptev/go-kit/metrics"
	"github.com/anatolykoptev/go-kit/metrics/mcpmw"
	linkedin "github.com/anatolykoptev/go-linkedin"
	"github.com/anatolykoptev/go-mcpserver"
	panelmcp "github.com/anatolykoptev/go-panel/mcp"
	"github.com/anatolykoptev/go-stealth/proxypool"
	twitter "github.com/anatolykoptev/go-twitter"
	"github.com/anatolykoptev/go-twitter/social"
	"github.com/anatolykoptev/go_job/internal/adminui"
	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/anatolykoptev/go_job/internal/engine/jobs/applications"
	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/anatolykoptev/go_job/internal/hunt/discovery"
	"github.com/anatolykoptev/go_job/internal/hunt/enrich"
	"github.com/anatolykoptev/go_job/internal/hunt/notify"
	"github.com/anatolykoptev/go_job/internal/huntworker"
	"github.com/anatolykoptev/go_job/internal/jobserver"
	"github.com/anatolykoptev/go_job/internal/oversize"
	"github.com/anatolykoptev/go_job/internal/pdfrender"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var (
	version          = "dev"
	mcpPort          = env.Str("MCP_PORT", "8891")
	fetchDirectFirst = env.Str("FETCH_DIRECT_FIRST", "auto")
)

func main() {
	// PF-6: install a redacting slog handler before any logging so the Telegram
	// bot token is never written to logs. When TELEGRAM_BOT_TOKEN is unset,
	// redaction is a no-op and the default handler is kept as-is.
	if token := os.Getenv("TELEGRAM_BOT_TOKEN"); token != "" {
		// Pass nil as the base handler so NewRedactingSlogHandler creates a
		// standalone TextHandler on os.Stderr with ReplaceAttr redaction.
		// Wrapping slog.Default().Handler() causes a deadlock: the default
		// handler writes via log.Logger.output, which calls back into the
		// installed slog default (our wrapper) → infinite recursion.
		slog.SetDefault(slog.New(notify.NewRedactingSlogHandler(nil, token)))
	}

	sigCtx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	huntNotifier := initEngine(sigCtx)

	slog.Info("starting go_job",
		slog.String("port", mcpPort),
	)

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

	// OBS-1: goroutine count gauge — updates every 15s for leak detection.
	engine.StartGoroutineCollector(sigCtx)

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

	hooks := mcpserver.MCPHooks{
		OnToolCall: func(_ context.Context, _ string) {
			engine.IncrToolCall()
		},
		OnToolResult: func(_ context.Context, name string, dur time.Duration, isErr bool) {
			slog.Info("tool_result", slog.String("tool", name), slog.Duration("duration", dur), slog.Bool("error", isErr))
		},
	}

	// BH-2: Wire BearerAuth when MCP_BEARER_TOKEN is set. Without this, any
	// client that can reach :8891/mcp has unauthenticated access to all 44
	// tools (job search, resume analysis, LLM scoring, DB writes). When the
	// token is unset, log a warning — acceptable for localhost-only deployments
	// but must not be exposed to untrusted networks without auth.
	var bearerAuth *mcpserver.BearerAuth
	if token := env.Str("MCP_BEARER_TOKEN", ""); token != "" {
		bearerAuth = &mcpserver.BearerAuth{
			Verifier:       mcpserver.StaticTokenVerifier(token),
			LoopbackBypass: true, // allow self-connect from same host
		}
		slog.Info("MCP BearerAuth enabled (loopback bypass on)")
	} else {
		slog.Warn("MCP_BEARER_TOKEN not set — MCP server running without authentication (acceptable for localhost-only)")
	}

	if err := mcpserver.Serve(&mcp.Implementation{
		Name:    "go_job",
		Version: version,
	}, mcpserver.Config{
		Name:                       "go_job",
		Version:                    version,
		Port:                       mcpPort,
		SchemaCache:                mcp.NewSchemaCache(),
		DisableLocalhostProtection: true,
		BearerAuth:                 bearerAuth,
		// SSE (text/event-stream) mode. Long tool calls (research, application_prep,
		// job_search, etc.) emit no bytes until they finish; in stateless mode the
		// server can't send ping requests, so a client/proxy idle-timeout would
		// abandon the call while the server keeps working. Instead,
		// ToolKeepaliveInterval emits a progress notification on the request
		// stream every 10s to keep it warm. (The old KeepAlive:30s ping was
		// inert in stateless mode — rejected, then closed the session at 30s and
		// truncated the response; removed.) Caddy forces HTTP/1.1 to the
		// upstream, fixing the h2 stream-reset that originally motivated JSON.
		JSONResponse:          false,
		ToolKeepaliveInterval: 10 * time.Second,
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
	}, func(s *mcp.Server) {
		jobserver.RegisterTools(s, authority)
		slog.Info("tools registered", slog.Int("count", 47))
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
func initEngine(sigCtx context.Context) hunt.Notifier {
	directFirst, initPool := resolveFetchMode(fetchDirectFirst)

	c := engine.Config{
		LLMAPIKey:                 env.Str("LLM_API_KEY", ""),
		LLMAPIKeyFallbacks:        env.List("LLM_API_KEY_FALLBACKS", ""),
		LLMAPIBase:                env.Str("LLM_API_BASE", "http://127.0.0.1:8317/v1"),
		LLMModel:                  env.Str("LLM_MODEL", "gemini-3.1-flash-lite-preview"),
		LLMModelFallback:          env.Str("LLM_MODEL_FALLBACK", ""),
		LLMProxyURLs:              env.List("LLM_PROXY_URLS", ""),
		LLMProxyKeys:              env.List("LLM_PROXY_KEYS", ""),
		LLMTemperature:            env.Float("LLM_TEMPERATURE", 0.1),
		LLMMaxTokens:              env.Int("LLM_MAX_TOKENS", 16384),
		MaxFetchURLs:              env.Int("MAX_FETCH_URLS", 8),
		MaxContentChars:           env.Int("MAX_CONTENT_CHARS", 6000),
		FetchTimeout:              env.Duration("FETCH_TIMEOUT", 10*time.Second),
		GithubToken:               env.Str("GITHUB_TOKEN", ""),
		CacheMaxEntries:           env.Int("CACHE_MAX_ENTRIES", 1000),
		CacheCleanupInterval:      env.Duration("CACHE_CLEANUP_INTERVAL", 300*time.Second),
		IndeedAPIKey:              env.Str("INDEED_API_KEY", ""),
		DatabaseURL:               env.Str("DATABASE_URL", ""),
		EmbedURL:                  env.Str("EMBED_URL", ""),
		OxBrowserURL:              env.Str("OX_BROWSER_URL", ""),
		CraigslistDefaultLocation: env.Str("CRAIGSLIST_DEFAULT_LOCATION", ""),
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

	// Initialize proxy pool: Webshare primary, Tor fallback (see initProxyPool).
	// Skipped when FETCH_DIRECT_FIRST=direct (direct-only) or FETCH_DIRECT_FIRST=off.
	c.ProxyPool = initProxyPool(initPool)

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

	// Validate CRAIGSLIST_DEFAULT_LOCATION at startup: an unmappable default
	// (e.g. "Salt Lake City") would, after the #347 token-boundary fix, surface
	// only as errCraigslistUnmapped on the first no-location search — fail fast
	// instead so the operator fixes the env value before any search runs.
	if err := jobs.ValidateCraigslistDefaultLocation(c.CraigslistDefaultLocation); err != nil {
		slog.Error("startup validation failed — exiting", slog.Any("error", err))
		os.Exit(1)
	}

	// OBS-6: wire the enrichment-skip metric hook so hunt package can bump
	// the gojob_enrich_semaphore_skipped_total counter without importing engine.
	hunt.SetEnrichSkipHook(engine.IncrEnrichSemSkipped)
	// OBS-6: wire the oversize purge metric hooks (avoids import cycle).
	oversize.SetPurgeMetricHooks(engine.IncrOversizePurgeDeleted, engine.IncrOversizePurgeErrors)

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

			// BH-3 / OBS-5: DB pool stats collector — exposes TotalConns,
			// IdleConns, and avg acquire wait time as Prometheus gauges.
			engine.StartDBPoolCollector(sigCtx, func() engine.PoolStatSnapshot {
				st := rdb.Pool().Stat()
				return engine.PoolStatSnapshot{
					TotalConns:      st.TotalConns(),
					IdleConns:       st.IdleConns(),
					AcquireCount:    st.AcquireCount(),
					AcquireDuration: st.AcquireDuration().Seconds(),
				}
			})

			// Wire oversize store on the same pool (fails-soft: optional spill feature).
			osStore := oversize.NewStore(rdb.Pool())
			if err := osStore.Migrate(context.Background()); err != nil {
				slog.Error("oversize migrate failed", slog.Any("error", err))
				// Non-fatal: oversize spill is optional; continue startup.
			} else {
				engine.SetOversizeStore(osStore)
				slog.Info("oversize store ready")
				// #185: auto-purge old oversize responses to prevent unbounded table growth.
				osStore.StartAutoPurge(sigCtx)
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
		engine.SetRawSearcher(searchClient)
		slog.Info("go-search client wired (ATS discovery + raw web search)",
			slog.String("go_search_url", goSearchURL),
		)
	} else {
		slog.Info("go-search: GO_SEARCH_URL unset — using local SearchDirect fallback for discovery and research")
	}

	// Embed clients (go-kit Embedder; auto-resolves EMBED_TOKEN from env).
	//
	// TWO clients hit the same embed server with DIFFERENT budgets:
	//
	//   - gateEmbedder: the relevance gate's OWN client, built via
	//     jobserver.NewEmbedClient so its per-request timeout, retry envelope,
	//     and chunk size are derived from JOB_SEARCH_RELEVANCE_TIMEOUT (the
	//     gate's inner budgets fit strictly inside its outer budget). Scoped to
	//     the gate via jobserver.SetRelevanceEmbedClient. See
	//     internal/jobserver/relevance_embed.go.
	//
	//   - sharedEmbedder: the package-level singleton consumed by algora ingest,
	//     resume-vector sync, and profile sync (jobs.SetEmbedClient). Built via
	//     kitembed.NewClient with ONLY the base opts so it keeps kitembed's
	//     library defaults (defaultRetryPolicy: 3 attempts; 30s per-request
	//     timeout). The gate's budgets MUST NOT leak onto these background jobs
	//     — they legitimately want retries and a long timeout (one 503 during
	//     resume ingest must retry, not fail on the first attempt).
	if c.EmbedURL != "" {
		baseOpts := []kitembed.Opt{
			kitembed.WithBackend("http"),
			kitembed.WithDim(1024),
			kitembed.WithLogger(slog.Default()),
		}
		// Both clients come from one call (jobserver.NewEmbedClients) so a
		// future edit cannot give one the other's options. See
		// internal/jobserver/embed_clients.go and
		// TestRelevanceEmbedBudget_WiringSplit.
		clients := jobserver.NewEmbedClients(c.EmbedURL, baseOpts...)
		if clients.GateErr != nil {
			slog.Error("gate embed client init failed", slog.Any("error", clients.GateErr))
		} else {
			jobserver.SetRelevanceEmbedClient(clients.Gate)
			slog.Info("gate embed client initialized (budget-bound to relevance timeout)",
				slog.String("url", c.EmbedURL))
			// Wire the cross-encoder shadow ONLY when the gate embed client
			// succeeded. The shadow observes the gate's cosine-scored
			// candidates; if the gate can't score (no embedder), the gate
			// returns early at the not-configured check and the shadow is
			// never invoked. Wiring it here makes the intent explicit: the
			// shadow runs only when the gate is functional.
			initCrossEncoderShadow(c.EmbedURL)
		}
		if clients.SharedErr != nil {
			slog.Error("shared embed client init failed", slog.Any("error", clients.SharedErr))
		} else {
			jobs.SetEmbedClient(clients.Shared)
			slog.Info("shared embed client initialized (library defaults)",
				slog.String("url", c.EmbedURL))
			// Off the startup path on purpose: this runs before the MCP
			// listener binds, and the deploy canary gives /health 30s. A
			// wedged embed backend would burn that whole budget here and the
			// deploy would roll back silently — the 2026-05-07 class. The
			// check reports, it does not gate.
			go checkEmbedCorpus(clients.Shared)
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

// checkEmbedCorpus verifies at startup that the active embed client is the one
// that built the corpus it is about to write into.
//
// Two checks, cheapest first. CheckCorpusDim compares dimensions — on
// resume_vectors the vector(1024) column already rejects a mismatch at write
// time, so this only moves the signal earlier. CheckCorpusConvention is the one
// that catches what nothing else does: a client at the right dimension that
// still produces different vectors, because the prefix convention, the model
// version or the backend changed. That failure is silent everywhere else —
// the write succeeds, the row count rises, and retrieval degrades quietly.
//
// Both are advisory. A drifted corpus degrades vector search; it does not make
// the service wrong to run, so refusing to start would trade a partial
// degradation for a total outage.
//
// Runs in its own goroutine (see the call site) and owns a Background context,
// so neither the embed round-trip nor a wedged backend can delay the listener.
// The timeout only bounds how long a stuck probe stays alive.
func checkEmbedCorpus(ec kitembed.Embedder) {
	rdb := jobs.GetResumeDB()
	if rdb == nil || ec == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := kitembed.CheckCorpusDim(ctx, ec, rdb); err != nil {
		var dimErr *kitembed.ErrCorpusDimMismatch
		if errors.As(err, &dimErr) {
			slog.Error("embed corpus dimension drift — new vectors will not match the existing corpus; re-embed before writing",
				slog.Int("embedder_dim", dimErr.EmbedderDim),
				slog.Int("corpus_dim", dimErr.CorpusDim))
		} else {
			slog.Warn("embed corpus dimension check did not complete", slog.Any("error", err))
		}
		return
	}

	if err := jobs.CheckCorpusConvention(ctx, rdb, ec); err != nil {
		var convErr *jobs.ErrCorpusConvention
		if errors.As(err, &convErr) {
			slog.Error("embed corpus convention drift — the active client does not reproduce the stored corpus; new vectors will not be comparable with existing ones",
				slog.Float64("cosine", convErr.Cosine),
				slog.Float64("floor", convErr.Min),
				slog.Int64("probe_vector_id", convErr.VectorID))
		} else {
			slog.Warn("embed corpus convention check did not complete", slog.Any("error", err))
		}
		return
	}

	slog.Info("embed corpus checks passed (dimension + convention)")
}

// initCrossEncoderShadow wires the cross-encoder (gte-multi-rerank) shadow
// client for the relevance gate. The reranker lives on the SAME host:port as
// the embedder (POST /v1/rerank), so it is wired from the same EMBED_URL. It
// is a SHADOW observer: it scores every listing the gate scores and records
// metrics, but NEVER changes the keep/reject decision (which stays on cosine).
// Failure is non-fatal and invisible to the caller. See
// internal/jobserver/relevance_rerank.go.
func initCrossEncoderShadow(embedURL string) {
	rerankClient := jobserver.NewRelevanceRerankClient(embedURL)
	jobserver.SetRelevanceRerankClient(rerankClient)
	if rerankClient != nil {
		slog.Info("cross-encoder shadow client initialized (shadow mode — decision stays on cosine)",
			slog.String("url", embedURL))
	}
}

// initProxyPool builds the fetch proxy pool: Webshare primary, optional Tor
// fallback. Returns nil (fetch direct) when the pool is disabled
// (initPool=false, i.e. FETCH_DIRECT_FIRST=direct/off) or when Webshare is
// unavailable and the Tor fallback is not explicitly enabled.
//
// The Tor fallback is OPT-IN via TOR_FALLBACK_ENABLED (default false), unlike
// go-wp where Tor is an unconditional fallback: go-job scrapes job boards
// (LinkedIn/ATS) that commonly hard-block Tor exit IPs, so silently routing a
// Webshare outage through Tor would turn into mass fetch failures. Operators
// enable it only for deployments whose targets tolerate Tor. When disabled and
// Webshare is unavailable, behaviour is unchanged from before: direct fetches.
func initProxyPool(initPool bool) proxypool.ProxyPool {
	if !initPool {
		return nil
	}
	if apiKey := env.Str("WEBSHARE_API_KEY", ""); apiKey != "" {
		pool, err := proxypool.NewWebshare(apiKey)
		if err != nil {
			slog.Warn("proxy pool: webshare init failed", slog.Any("error", err))
		} else {
			slog.Info("proxy pool: using Webshare", slog.Int("proxies", pool.Len()))
			return pool
		}
	}
	if env.Bool("TOR_FALLBACK_ENABLED", false) {
		torProxy := env.Str("TOR_PROXY", "socks5://tor:9050")
		slog.Info("proxy pool: using Tor fallback", slog.String("proxy", torProxy))
		return proxypool.NewStatic(torProxy)
	}
	slog.Warn("proxy pool: webshare unavailable, fetching direct (set TOR_FALLBACK_ENABLED=true to route via Tor)")
	return nil
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
// (default 8896, host-restricted to 127.0.0.1 by compose), mounted at /admin,
// and optionally the admin MCP server on ADMIN_MCP_PORT (default 8897) which
// auto-exposes all registered Resources as MCP list/get tools. Fail-soft: when
// admin credentials are unset (adminui.New returns ok=false) both are skipped,
// so deploying before the env is wired changes nothing.
func startAdminServer(ctx context.Context, store *hunt.Store, authority *applications.Authority, logger *slog.Logger) {
	handler, panel, ok := adminui.New(store, authority)
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
	mux.Handle("/admin/", engine.AdminMetricsMiddleware(handler))
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

	// Admin MCP server: auto-exposes registered Resources as MCP tools.
	// Runs on a separate port so it can be independently gated/authed.
	mcpPort := env.Str("ADMIN_MCP_PORT", "8897")
	go func() {
		logger.Info("admin MCP endpoint", slog.String("addr", mcpPort))
		if err := panelmcp.Run(panelmcp.Config{
			Panel:   panel,
			Port:    mcpPort,
			Context: ctx,
			Logger:  logger,
		}); err != nil {
			logger.Error("admin MCP server", slog.Any("error", err))
		}
	}()
}
