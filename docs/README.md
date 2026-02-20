# go_job MCP — Documentation

**go_job** is a standalone Go MCP server exposing **11 tools** for job search, resume optimization, career research, and application tracking.

- **MCP endpoint:** `http://localhost:8891/mcp`
- **Health:** `http://localhost:8891/health`
- **Metrics:** `http://localhost:8891/metrics`
- **Transport:** HTTP (Streamable) or `--stdio`

---

## Tools

### Search

| Tool | Description | Doc |
|------|-------------|-----|
| `job_search` | LinkedIn, Greenhouse, Lever, YC, HN, Indeed, Хабр (10+ sources) | [→ tools/job_search.md](tools/job_search.md) |
| `remote_work_search` | RemoteOK, WeWorkRemotely, SearXNG | [→ tools/remote_work_search.md](tools/remote_work_search.md) |
| `freelance_search` | Upwork, Freelancer.com | [→ tools/freelance_search.md](tools/freelance_search.md) |

### Resume

| Tool | Description | Doc |
|------|-------------|-----|
| `resume_analyze` | ATS score (0–100), missing keywords, gaps, recommendations | [→ tools/resume_analyze.md](tools/resume_analyze.md) |
| `cover_letter_generate` | Tailored cover letter (3 tones: professional / friendly / concise) | [→ tools/cover_letter_generate.md](tools/cover_letter_generate.md) |
| `resume_tailor` | Rewrite resume sections to match JD, keyword diff | [→ tools/resume_tailor.md](tools/resume_tailor.md) |

### Research

| Tool | Description | Doc |
|------|-------------|-----|
| `salary_research` | p25 / median / p75 benchmarks, RU + international | [→ tools/salary_research.md](tools/salary_research.md) |
| `company_research` | Size, funding, tech stack, culture, Glassdoor rating, news | [→ tools/company_research.md](tools/company_research.md) |

### Tracker

| Tool | Description | Doc |
|------|-------------|-----|
| `job_tracker_add` | Save job to local SQLite tracker | [→ tools/job_tracker_add.md](tools/job_tracker_add.md) |
| `job_tracker_list` | List tracked jobs, filter by status | [→ tools/job_tracker_list.md](tools/job_tracker_list.md) |
| `job_tracker_update` | Update status / notes by ID | [→ tools/job_tracker_update.md](tools/job_tracker_update.md) |

---

## Architecture

```
go_job/
├── main.go                          # HTTP/stdio MCP server, engine init
├── internal/
│   ├── engine/                      # Core engine (cache, LLM, search, HTTP)
│   │   ├── cache.go                 # 2-tier cache: L1 in-memory + L2 Redis
│   │   ├── config.go                # Engine config struct
│   │   ├── llm.go                   # LLM calls (OpenAI-compatible API)
│   │   ├── search.go                # SearXNG integration
│   │   ├── fetch_html.go            # URL content fetching + readability
│   │   ├── retry.go                 # HTTP retry with backoff
│   │   ├── metrics.go               # Prometheus-style counters
│   │   ├── types_jobs.go            # Job/Remote/Freelance input+output types
│   │   ├── jobs/                    # Job source + career tool implementations
│   │   │   ├── linkedin.go          # LinkedIn Guest API + JSON-LD detail fetch
│   │   │   ├── ats.go               # Greenhouse + Lever public APIs
│   │   │   ├── hnjobs.go            # HN "Who is Hiring?" via Algolia + Firebase
│   │   │   ├── ycjobs.go            # YC workatastartup.com
│   │   │   ├── remotejobs.go        # RemoteOK API + WeWorkRemotely RSS
│   │   │   ├── indeed.go            # Indeed via SearXNG + JSON-LD scrape
│   │   │   ├── habr.go              # Хабр Карьера public JSON API
│   │   │   ├── resume.go            # resume_analyze, cover_letter_generate, resume_tailor
│   │   │   ├── research.go          # salary_research, company_research
│   │   │   └── tracker.go           # job_tracker_add/list/update (SQLite)
│   │   └── sources/
│   │       └── freelancer.go        # Freelancer.com REST API
│   ├── jobserver/
│   │   └── register.go              # MCP tool registrations (11 tools)
│   └── toolutil/
│       └── toolutil.go              # Shared helpers: cache, fetch, lang
├── docs/
│   ├── README.md                    # Index + architecture
│   ├── compare.md                   # Comparison with competitors
│   ├── roadmap.md                   # Feature roadmap
│   └── tools/                       # Per-tool documentation
│       ├── job_search.md
│       ├── remote_work_search.md
│       ├── freelance_search.md
│       ├── resume_analyze.md
│       ├── cover_letter_generate.md
│       ├── resume_tailor.md
│       ├── salary_research.md
│       ├── company_research.md
│       ├── job_tracker_add.md
│       ├── job_tracker_list.md
│       └── job_tracker_update.md
└── deploy/
    └── go_job.service               # systemd unit
```

## Data Storage

| Store | Location | Purpose |
|-------|----------|---------|
| Job tracker | `~/.go_job/tracker.db` | SQLite, persists across restarts |
| L1 cache | in-memory (`sync.Map`) | Fast, lost on restart |
| L2 cache | Redis (optional) | Persistent, shared across instances |

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `MCP_PORT` | `8891` | HTTP listen port |
| `LLM_API_KEY` | — | LLM API key (Gemini/OpenAI-compatible) |
| `LLM_API_BASE` | `https://generativelanguage.googleapis.com/v1beta/openai` | LLM API base URL |
| `LLM_MODEL` | `gemini-2.5-flash` | Model name |
| `LLM_TEMPERATURE` | `0.1` | Sampling temperature |
| `LLM_MAX_TOKENS` | `16384` | Max output tokens |
| `SEARXNG_URL` | `http://127.0.0.1:8888` | SearXNG instance URL |
| `REDIS_URL` | — | Redis URL for L2 cache (optional) |
| `CACHE_TTL` | `900` (15m) | Cache TTL in seconds |
| `MAX_FETCH_URLS` | `8` | Max parallel URL fetches |
| `MAX_CONTENT_CHARS` | `6000` | Max chars per fetched page |
| `FETCH_TIMEOUT` | `10` | HTTP fetch timeout in seconds |
| `GITHUB_TOKEN` | — | GitHub token (reserved) |

## Caching

Results are cached at two levels:

- **L1 (in-memory):** `sync.Map` with TTL eviction. Fast, lost on restart.
- **L2 (Redis):** Optional. Survives restarts, shared across instances.

Cache key format: `sha256(tool_name + "|" + param1 + "|" + param2 + ...)`.

**Note:** `resume_analyze`, `cover_letter_generate`, `resume_tailor`, `salary_research`, `company_research` are **not cached** (LLM-generated, context-dependent). Job tracker operations use SQLite directly.

## Rate Limiting & Anti-bot

- **LinkedIn:** Uses `bogdanfinn/tls-client` for Chrome TLS fingerprint when `BrowserClient` is configured. Falls back to standard `net/http` with Chrome User-Agent.
- **HN Firebase:** Max 10 concurrent requests, staggered delays per batch.
- **LinkedIn detail fetch:** Staggered 1s delays between parallel fetches.
- **All sources:** HTTP retry with exponential backoff (`engine.RetryHTTP`).
