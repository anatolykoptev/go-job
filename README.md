# go_job

Job, Remote & Freelance Search MCP server. Exposes 4 MCP tools for structured job discovery across major platforms.

## MCP Tools

| Tool | Sources | Description |
|------|---------|-------------|
| `job_search` | LinkedIn, Greenhouse, Lever, YC, HN, Indeed, Habr | Structured job search with filters (experience, type, remote, salary, Easy Apply). Returns up to 15 deduplicated jobs with salary, skills, description. |
| `remote_work_search` | RemoteOK, WeWorkRemotely, Remotive, SearXNG | Remote-first job search. Returns structured listings with salary, tags, source. |
| `freelance_search` | Freelancer.com (direct API), Upwork (SearXNG) | Freelance project search. Freelancer API returns budgets, skills, bids directly. |
| `job_match_score` | LinkedIn, Indeed, YC, HN | Score job listings against a resume using Jaccard keyword overlap (0–100). Returns jobs sorted by match score with matching/missing keywords. |

## Filters (job_search)

| Filter | Values |
|--------|--------|
| `experience` | internship, entry, associate, mid-senior, director, executive |
| `job_type` | full-time, part-time, contract, temporary |
| `remote` | onsite, hybrid, remote |
| `time_range` | day, week, month |
| `salary` | 40k+, 60k+, 80k+, 100k+, 120k+, 140k+, 160k+, 180k+, 200k+ |
| `easy_apply` | true (LinkedIn Easy Apply only) |
| `platform` | linkedin, greenhouse, lever, ats, yc, hn, indeed, habr, startup, all (default) |

## Architecture

```
go_job/
├── main.go
├── internal/
│   ├── engine/
│   │   ├── config.go          # Config struct + Init()
│   │   ├── types_jobs.go      # Input/output types for all 4 tools
│   │   ├── prompt.go          # LLM instructions (JobSearchInstruction, etc.)
│   │   ├── llm.go             # LLM calls + SummarizeJobResults
│   │   ├── textutil.go        # CanonicalJobKey, TruncateRunes, user agents
│   │   ├── cache.go           # 2-tier cache: L1 in-memory + L2 Redis
│   │   ├── search.go          # SearchSearXNG, DedupByDomain
│   │   ├── fetch.go           # FetchURLContent (readability + html→markdown)
│   │   ├── httpclient.go      # BrowserClient (Chrome TLS fingerprint via bogdanfinn)
│   │   ├── retry.go           # RetryHTTP, RetryDo (exponential backoff)
│   │   └── jobs/
│   │       ├── linkedin.go    # LinkedIn Guest API + JSON-LD + geo_id + pagination
│   │       ├── indeed.go      # Indeed iOS GraphQL API + SearXNG fallback
│   │       ├── remotejobs.go  # RemoteOK + WeWorkRemotely + Remotive APIs
│   │       ├── habr.go        # Habr Карьера scraper
│   │       ├── hnjobs.go      # HN Who is Hiring (Algolia)
│   │       ├── ycjobs.go      # YC workatastartup.com
│   │       ├── ats.go         # Greenhouse + Lever ATSes
│   │       ├── match.go       # Jaccard keyword scoring (job_match_score)
│   │       ├── resume.go      # resume_analyze, cover_letter_generate, resume_tailor
│   │       ├── research.go    # company_research
│   │       └── tracker.go     # Job application tracker (SQLite)
│   ├── jobserver/
│   │   └── register.go        # Tool registrations (all 4 MCP tools)
│   └── toolutil/
│       └── toolutil.go        # Cache helpers, FetchURLsParallel, NormLang
└── deploy/
    └── go_job.service         # systemd unit (port 8891)
```

## Key Implementation Details

### LinkedIn
- **Guest API** — no auth, Chrome TLS fingerprint via `bogdanfinn/tls-client`
- **Pagination** — 25-result pages, up to `maxResults=50`
- **geo_id** — 42 known locations (cities + countries) → precise LinkedIn geoId filter
- **Easy Apply** — `f_JIYN=true` param, exposed as `easy_apply` input field
- **JSON-LD** — fetches `schema.org/JobPosting` from top 8 jobs for full descriptions

### Indeed
- **GraphQL API** — internal iOS app endpoint (`apis.indeed.com/graphql`)
- **Key** — loaded from `INDEED_API_KEY` env (no hardcode)
- **Salary** — structured from `baseSalary` or `estimated.baseSalary` range
- **Fallback** — SearXNG `site:indeed.com/viewjob` if GraphQL fails

### Remotive
- **Free public API** — `remotive.com/api/remote-jobs?search=...`, no auth
- Added to `remote_work_search` alongside RemoteOK + WeWorkRemotely

### Deduplication
1. URL dedup (exact match)
2. Canonical key dedup — normalizes title (strips "at CompanyName"), collapses non-alphanumeric

### Structured Salary (JobListing)
```json
{
  "salary": "$80k–120k USD/yr",
  "salary_min": 80000,
  "salary_max": 120000,
  "salary_currency": "USD",
  "salary_interval": "year"
}
```

## Running

```bash
# HTTP mode (default port 8891)
MCP_PORT=8891 LLM_API_KEY=... INDEED_API_KEY=... ./bin/go_job

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
| `LLM_MODEL` | `gemini-2.5-flash` | Model name |
| `MCP_PORT` | `8891` | HTTP server port |
| `INDEED_API_KEY` | (required for Indeed) | iOS app key — set in `.env` |
| `REDIS_URL` | (optional) | Redis for L2 cache |
| `CACHE_TTL` | `900` | Cache TTL in seconds |
| `FETCH_TIMEOUT` | `15` | URL fetch timeout in seconds |

## Health check

```bash
curl http://localhost:8891/health
# {"status":"ok","service":"go_job","version":"1.0.0"}
```
