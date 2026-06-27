# go_job MCP — Documentation

**go_job** is a standalone Go MCP server exposing **28 tools** for job search, resume optimization, career research, and application tracking.

- **MCP endpoint:** `http://localhost:8891/mcp`
- **Health:** `http://localhost:8891/health`
- **Metrics:** `http://localhost:9891/metrics`
- **Transport:** HTTP (Streamable) or `--stdio`

---

## Tools

### Search

| Tool | Description | Doc |
|------|-------------|-----|
| `job_search` | LinkedIn, Greenhouse, Lever, YC, HN, Indeed, Хабр, RemoteOK, WeWorkRemotely, Remotive, Twitter/X, Google Jobs (15+ sources). Supports `limit` (default 15, max 50), `offset` (pagination), `platform` filter, `blacklist`. | [→ tools/job_search.md](tools/job_search.md) |
| `job_match_score` | Score job listings against a resume (Jaccard 0–100) | [→ tools/job_match_score.md](tools/job_match_score.md) |
| `opportunity_search` | Cross-type opportunity search (jobs + freelance + bounty) | — |
| `opportunity_analyze` | Deep-analyze a single opportunity URL | — |
| `opportunity_claim` | Initiate a claim on a matched opportunity | — |
| `ats` | Direct ATS board fetch (Greenhouse, Lever, Ashby) | — |

### Resume

| Tool | Description | Doc |
|------|-------------|-----|
| `resume_analyze` | ATS score (0–100), missing keywords, gaps, recommendations | [→ tools/resume_analyze.md](tools/resume_analyze.md) |
| `cover_letter_generate` | Tailored cover letter (3 tones: professional / friendly / concise) | [→ tools/cover_letter_generate.md](tools/cover_letter_generate.md) |
| `resume_tailor` | Rewrite resume sections to match JD, keyword diff | [→ tools/resume_tailor.md](tools/resume_tailor.md) |
| `master_resume_build` | Build a master resume profile from raw experience | — |
| `resume_generate` | Generate a targeted resume from the master profile | — |
| `resume_enrich` | Enrich master profile via Q&A | — |
| `resume_profile` | View the stored master profile | — |
| `resume_memory` | Semantic search/add/update over resume memory store | — |

### Research

| Tool | Description | Doc |
|------|-------------|-----|
| `research` | Research salary, company, or person. `subject=salary\|company\|person` | [→ tools/salary_research.md](tools/salary_research.md) (salary), [→ tools/company_research.md](tools/company_research.md) (company) |

### Interview & Career Prep

| Tool | Description | Doc |
|------|-------------|-----|
| `interview_prep` | Personalized Q&A (behavioral + technical + system design) with model answers | — |
| `project_showcase` | STAR-format project narratives with impact and talking points | — |
| `pitch_generate` | 30-sec & 2-min elevator pitches, "why this company" answer | — |
| `skill_gap` | Resume vs JD gap: match score, missing skills, learning plan | — |

### Application Workflow

| Tool | Description | Doc |
|------|-------------|-----|
| `application_prep` | One-call combo: analyze + cover letter + interview prep + company research | — |
| `offer_compare` | Side-by-side offer comparison with scoring (0–100) | — |
| `negotiation_prep` | Salary negotiation playbook: scripts, counters, BATNA | — |
| `linkedin` | LinkedIn profile operations | — |
| `linkedin_profile_ingest` | Ingest a LinkedIn profile for local analysis | — |
| `algora_job_ingest` | Ingest Algora bounty/job listings into the hunt store | — |

### Tracker & Utilities

| Tool | Description | Doc |
|------|-------------|-----|
| `job_tracker` | Track applications. `action=add` (title+company required) \| `action=list` \| `action=update` (id required) | [→ tools/job_tracker_add.md](tools/job_tracker_add.md) |
| `hunt_list` | List hunt entries from the local store (triggers lazy enrichment) | — |
| `oversize` | Retrieve / list / purge oversized MCP responses from the spillover store | — |

---

## Architecture

```
go_job/
├── main.go                          # HTTP/stdio MCP server, engine init
├── internal/
│   ├── engine/                      # Core engine (cache, LLM, search, HTTP)
│   │   ├── bridge.go                # Source bridge wiring
│   │   ├── bridge_jobs.go           # Job-specific bridge helpers
│   │   ├── bridge_llm.go            # LLM bridge
│   │   ├── cache.go                 # 2-tier cache: L1 in-memory + L2 Redis
│   │   ├── config.go                # Engine config struct
│   │   ├── search.go                # SearXNG integration
│   │   ├── metrics.go               # Prometheus-style counters
│   │   ├── pipeline.go              # Fan-out pipeline
│   │   ├── types_jobs.go            # Job/Remote/Freelance input+output types
│   │   ├── jobs/                    # Job source + career tool implementations
│   │   │   ├── linkedin.go          # LinkedIn Guest API + JSON-LD detail fetch
│   │   │   ├── ats.go               # Greenhouse + Lever + Ashby ATSes
│   │   │   ├── hnjobs.go            # HN "Who is Hiring?" via Algolia
│   │   │   ├── ycjobs.go            # YC workatastartup.com
│   │   │   ├── remotejobs.go        # RemoteOK API + WeWorkRemotely RSS
│   │   │   ├── indeed.go            # Indeed via iOS GraphQL API + SearXNG fallback
│   │   │   ├── habr.go              # Хабр Карьера public JSON API
│   │   │   ├── resume.go            # resume_analyze, cover_letter_generate, resume_tailor
│   │   │   ├── research.go          # research tool (salary / company / person)
│   │   │   ├── tracker.go           # job_tracker (SQLite, UPLOADS_ROOT)
│   │   │   └── profile.go           # user profile (UPLOADS_ROOT)
│   │   └── sources/                 # Pluggable source connectors
│   │       ├── freelancer.go        # Freelancer.com REST API
│   │       ├── github.go            # GitHub source
│   │       ├── hackernews.go        # HN source
│   │       └── ...
│   ├── jobserver/
│   │   ├── register.go              # MCP tool registrations (28 tools)
│   │   └── tool_*.go                # Per-tool handler files
│   └── hunt/                        # Hunt store + notifications
│       ├── notify/
│       │   └── telegram.go          # Telegram notifications via go-kit ProductSink
│       └── ...
├── docs/
│   ├── README.md                    # This file — index + architecture
│   ├── compare.md                   # Comparison with competitors
│   ├── roadmap.md                   # Feature roadmap
│   └── tools/                       # Per-tool documentation
│       ├── job_search.md
│       ├── job_match_score.md
│       ├── resume_analyze.md
│       ├── cover_letter_generate.md
│       ├── resume_tailor.md
│       ├── salary_research.md
│       ├── company_research.md
│       └── job_tracker_add.md
└── deploy/
    └── go_job.service               # systemd unit (MCP :8891, metrics :9891)
```

## Data Storage

| Store | Location | Purpose |
|-------|----------|---------|
| Job tracker | `$UPLOADS_ROOT/go-job/tracker/tracker.db` (default `$HOME/uploads/go-job/tracker/tracker.db`) | SQLite, table `jobs`, persists across restarts |
| User profile | `$UPLOADS_ROOT/go-job/profile/profile.json` (default `$HOME/uploads/go-job/profile/profile.json`) | Default platform, limit, location, remote, blacklist |
| L1 cache | in-memory (`sync.Map`) | Fast, lost on restart |
| L2 cache | Redis (optional) | Persistent, shared across instances |

Override the base directory via `UPLOADS_ROOT` env var.

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `MCP_PORT` | `8891` | MCP HTTP listen port |
| `PROM_PORT` | `9891` | Prometheus metrics port |
| `LLM_API_KEY` | — | LLM API key (Gemini/OpenAI-compatible) |
| `LLM_API_BASE` | `https://generativelanguage.googleapis.com/v1beta/openai` | LLM API base URL |
| `LLM_MODEL` | `gemini-3.1-flash-lite-preview` | Model name |
| `LLM_TEMPERATURE` | `0.1` | Sampling temperature |
| `LLM_MAX_TOKENS` | `16384` | Max output tokens |
| `SEARXNG_URL` | `http://127.0.0.1:8888` | SearXNG instance URL |
| `REDIS_URL` | — | Redis URL for L2 cache (optional) |
| `CACHE_TTL` | `900` (15m) | Cache TTL in seconds |
| `UPLOADS_ROOT` | `$HOME/uploads` | Base dir for tracker DB + user profile |
| `MAX_FETCH_URLS` | `8` | Max parallel URL fetches |
| `MAX_CONTENT_CHARS` | `6000` | Max chars per fetched page |
| `FETCH_TIMEOUT` | `10` | HTTP fetch timeout in seconds |
| `DATABASE_URL` | — | Postgres for oversized payload spillover (optional) |
| `TELEGRAM_BOT_TOKEN` | — | Telegram bot token for hunt notifications |
| `HUNT_NOTIFY_CHAT_ID` | — | Notification recipient chat ID |
| `GITHUB_TOKEN` | — | GitHub token (optional, improves rate limits) |

## Caching

Results are cached at two levels:

- **L1 (in-memory):** `sync.Map` with TTL eviction. Fast, lost on restart.
- **L2 (Redis):** Optional. Survives restarts, shared across instances.

Cache key format: `sha256(tool_name + "|" + param1 + "|" + param2 + ...)`.

**Not cached:** `resume_analyze`, `cover_letter_generate`, `resume_tailor`, `research` (LLM-generated, context-dependent). Job tracker operations use SQLite directly.

## Rate Limiting & Anti-bot

- **LinkedIn:** Uses `bogdanfinn/tls-client` for Chrome TLS fingerprint when `BrowserClient` is configured. Falls back to standard `net/http` with Chrome User-Agent.
- **HN Firebase:** Max 10 concurrent requests, staggered delays per batch.
- **LinkedIn detail fetch:** Staggered 1s delays between parallel fetches.
- **All sources:** HTTP retry with exponential backoff.
