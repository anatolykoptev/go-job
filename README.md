# go_job

Job search, resume optimization, career research, and application tracking — a Go MCP server exposing **28 tools** across LinkedIn, Greenhouse, Lever, YC, HN, Indeed, Хабр, RemoteOK, WeWorkRemotely, Twitter/X, Google Jobs, and more.

## MCP Tools

### Search

| Tool | Description |
|------|-------------|
| `job_search` | Unified search: LinkedIn, Greenhouse, Lever, YC, HN, Indeed, Хабр, RemoteOK, WeWorkRemotely, Remotive, Twitter/X, Google Jobs. `platform=` selects source; `limit` (default 15, max 50) + `offset` for pagination. |
| `job_match_score` | Score job listings against a resume using Jaccard keyword overlap (0–100). |
| `opportunity_search` | Cross-type opportunity search (jobs + freelance + bounty). |
| `opportunity_analyze` | Deep analyze a single opportunity URL. |
| `opportunity_claim` | Initiate a claim action on a matched opportunity. |

### Resume

| Tool | Description |
|------|-------------|
| `resume_analyze` | ATS score (0–100), missing keywords, gaps, recommendations. |
| `cover_letter_generate` | Tailored cover letter (3 tones: professional / friendly / concise). |
| `resume_tailor` | Rewrite resume sections to match JD, keyword diff. |
| `master_resume_build` | Build a master resume profile from raw experience. |
| `resume_generate` | Generate a targeted resume from the master profile. |
| `resume_enrich` | Enrich master profile via Q&A. |
| `resume_profile` | View the stored master profile. |
| `resume_memory` | Semantic search/add/update over resume memory store. |

### Research

| Tool | Description |
|------|-------------|
| `research` | Research salary, company, or person. `subject=salary\|company\|person`. |
| `ats` | Direct ATS board fetch (Greenhouse, Lever, Ashby). |

### Interview & Career Prep

| Tool | Description |
|------|-------------|
| `interview_prep` | Personalized Q&A (behavioral + technical + system design) with model answers. |
| `project_showcase` | STAR-format project narratives with impact and talking points. |
| `pitch_generate` | 30-sec & 2-min elevator pitches, "why this company" answer. |
| `skill_gap` | Resume vs JD gap: match score, missing skills, learning plan. |

### Application Workflow

| Tool | Description |
|------|-------------|
| `application_prep` | One-call combo: resume analysis + cover letter + interview prep + company research. |
| `offer_compare` | Side-by-side offer comparison with scoring (0–100). |
| `negotiation_prep` | Salary negotiation playbook: scripts, counters, BATNA. |
| `linkedin` | LinkedIn profile operations. |
| `linkedin_profile_ingest` | Ingest a LinkedIn profile for local analysis. |

### Tracker & Utilities

| Tool | Description |
|------|-------------|
| `job_tracker` | Track job applications. `action=add\|list\|update`. |
| `algora_job_ingest` | Ingest Algora bounty/job listings into the hunt store. |
| `hunt_list` | List hunt entries from the local store (triggers lazy enrichment). |
| `oversize` | Retrieve / list / purge oversized MCP responses from the spillover store. |

## Filters (job_search)

| Filter | Values |
|--------|--------|
| `experience` | internship, entry, associate, mid-senior, director, executive |
| `job_type` | full-time, part-time, contract, temporary |
| `remote` | onsite, hybrid, remote |
| `time_range` | day, week, month |
| `salary` | 40k+, 60k+, 80k+, 100k+, 120k+, 140k+, 160k+, 180k+, 200k+ |
| `easy_apply` | true (LinkedIn Easy Apply only) |
| `platform` | linkedin, greenhouse, lever, ats, yc, hn, indeed, habr, remoteok, weworkremotely, remotive, twitter, google, un, inspira, undp, all (default) |
| `limit` | 1–50 (default 15) |
| `offset` | skip N results for pagination |
| `blacklist` | comma-separated company/keyword exclusion |

## Architecture

```
go_job/
├── main.go
├── internal/
│   ├── engine/
│   │   ├── config.go          # Config struct + Init()
│   │   ├── bridge.go          # Source bridge wiring
│   │   ├── bridge_jobs.go     # Job-specific bridge helpers
│   │   ├── bridge_llm.go      # LLM bridge
│   │   ├── cache.go           # 2-tier cache: L1 in-memory + L2 Redis
│   │   ├── search.go          # SearchSearXNG, DedupByDomain
│   │   ├── metrics.go         # Prometheus counters/histograms
│   │   ├── pipeline.go        # Fan-out pipeline helpers
│   │   ├── types_jobs.go      # Input/output types
│   │   ├── prompt.go          # LLM instructions (shared)
│   │   ├── prompt_jobs.go     # Job-specific LLM prompts
│   │   └── jobs/              # Job source + career tool implementations
│   │       ├── linkedin.go    # LinkedIn Guest API + JSON-LD + geo_id + pagination
│   │       ├── indeed.go      # Indeed iOS GraphQL API + SearXNG fallback
│   │       ├── remotejobs.go  # RemoteOK + WeWorkRemotely + Remotive APIs
│   │       ├── habr.go        # Habr Карьера scraper
│   │       ├── hnjobs.go      # HN Who is Hiring (Algolia)
│   │       ├── ycjobs.go      # YC workatastartup.com
│   │       ├── ats.go         # Greenhouse + Lever + Ashby ATSes
│   │       ├── match.go       # Jaccard keyword scoring (job_match_score)
│   │       ├── resume.go      # resume_analyze, cover_letter_generate, resume_tailor
│   │       ├── research.go    # research tool (salary / company / person)
│   │       ├── tracker.go     # Job application tracker (SQLite)
│   │       ├── profile.go     # User profile persistence
│   │       └── ...            # algora, twitter, linkedin, bounty, opportunity, etc.
│   │   └── sources/           # Pluggable source connectors
│   │       ├── freelancer.go  # Freelancer.com REST API
│   │       ├── github.go      # GitHub jobs / PRs
│   │       ├── hackernews.go  # HN source
│   │       └── ...
│   ├── jobserver/
│   │   ├── register.go        # Tool registrations (28 MCP tools)
│   │   └── tool_*.go          # Per-tool handler files
│   └── hunt/                  # Hunt store + notifications
│       ├── notify/
│       │   └── telegram.go    # Telegram notifications via go-kit ProductSink
│       └── ...
└── deploy/
    └── go_job.service         # systemd unit (MCP :8891, metrics :9891)
```

## Key Implementation Details

### job_search
- **Unified platform**: `remote_work_search` and `freelance_search` and `twitter_job_search` are folded into `job_search` via `platform=remoteok|weworkremotely|remotive|twitter`. Use `raw=true` with `platform=twitter` to skip LLM processing.
- **LinkedIn**: no auth, Chrome TLS fingerprint via `bogdanfinn/tls-client`; pagination with `offset`; 42 geo locations.
- **Google Jobs**: `platform=google` via SearXNG.
- **UN sources**: `platform=inspira` (careers.un.org) / `platform=undp` / `platform=un` (fan-out both). Not included in `platform=all`.

### research tool
- Replaces three separate tools (`salary_research`, `company_research`, `person_research`).
- `subject=salary` (role required), `subject=company` (company required), `subject=person` (name required).

### job_tracker
- Single tool replaces `job_tracker_add`, `job_tracker_list`, `job_tracker_update`.
- `action=add` (title+company required), `action=list`, `action=update` (id required).

### Data Storage
- **Job tracker DB**: `$UPLOADS_ROOT/go-job/tracker/tracker.db` (SQLite, table `jobs`). Default path: `$HOME/uploads/go-job/tracker/tracker.db`. Override via `UPLOADS_ROOT`.
- **User profile**: `$UPLOADS_ROOT/go-job/profile/profile.json`. Default: `$HOME/uploads/go-job/profile/profile.json`.
- **L1 cache**: in-memory (`sync.Map`), lost on restart.
- **L2 cache**: Redis (optional), persistent.

### Notifications
- New hunt entries trigger Telegram notifications via `internal/hunt/notify/telegram.go` using go-kit `ProductSink` (own bot, rate-limited fan-out).
- Requires `TELEGRAM_BOT_TOKEN` + `HUNT_NOTIFY_CHAT_ID` env vars.

## Running

```bash
# HTTP mode (default MCP port 8891, metrics port 9891)
MCP_PORT=8891 PROM_PORT=9891 LLM_API_KEY=... ./bin/go_job

# stdio mode
./bin/go_job --stdio
```

## Build & Deploy

```bash
make build    # → bin/go_job
make deploy   # build + copy service + restart systemd unit
make restart  # restart only
```

## Config (env vars)

| Var | Default | Description |
|-----|---------|-------------|
| `SEARXNG_URL` | `http://127.0.0.1:8888` | SearXNG instance |
| `LLM_API_KEY` | (required) | Gemini/OpenAI-compatible API key |
| `LLM_API_BASE` | Gemini endpoint | OpenAI-compatible base URL |
| `LLM_MODEL` | `gemini-3.1-flash-lite-preview` | Model name |
| `MCP_PORT` | `8891` | MCP HTTP server port |
| `PROM_PORT` | `9891` | Prometheus metrics port |
| `REDIS_URL` | (optional) | Redis for L2 cache |
| `CACHE_TTL` | `900` | Cache TTL in seconds |
| `UPLOADS_ROOT` | `$HOME/uploads` | Base dir for tracker DB + user profile |
| `DATABASE_URL` | (optional) | Postgres for oversized payload spillover |
| `TELEGRAM_BOT_TOKEN` | (required for notifications) | Telegram bot token |
| `HUNT_NOTIFY_CHAT_ID` | (required for notifications) | Notification recipient chat ID |

## Health check

```bash
curl http://localhost:8891/health
# {"status":"ok","service":"go_job","version":"1.0.0"}
```

## Metrics

```bash
curl http://localhost:9891/metrics
```

## Spillover store

Large MCP responses can exceed the MCP envelope limit (~25KB). go-job spills oversized payloads to a Postgres table `oversize_responses` (auto-migrated on startup).

When a tool response exceeds `GO_JOB_OVERSIZE_THRESHOLD_BYTES` (default 24576), the client receives a small envelope with `oversize_id` and a `sample`. Use the `oversize` tool to retrieve the full payload.

Requires `DATABASE_URL`. Gracefully falls back to direct response if unset.
