# go_job MCP — Documentation

**go_job** is a standalone Go MCP server exposing three tools for job, remote work, and freelance search across multiple platforms.

- **MCP endpoint:** `http://localhost:8891/mcp`
- **Health:** `http://localhost:8891/health`
- **Metrics:** `http://localhost:8891/metrics`
- **Transport:** HTTP (Streamable) or `--stdio`

---

## Tools

### `job_search`

Search for job listings across LinkedIn, Greenhouse, Lever, YC workatastartup.com, HN Who is Hiring, and Indeed.

#### Input

| Parameter    | Type   | Required | Description |
|-------------|--------|----------|-------------|
| `query`     | string | ✅       | Job search keywords (e.g. `golang developer`, `data engineer`) |
| `location`  | string | —        | City, country, or `Remote` (e.g. `Berlin`, `United States`) |
| `experience`| string | —        | `internship` \| `entry` \| `associate` \| `mid-senior` \| `director` \| `executive` |
| `job_type`  | string | —        | `full-time` \| `part-time` \| `contract` \| `temporary` |
| `remote`    | string | —        | `onsite` \| `hybrid` \| `remote` |
| `time_range`| string | —        | `day` \| `week` \| `month` |
| `platform`  | string | —        | Source filter — see table below |
| `language`  | string | —        | Answer language code (default: `all`) |

#### Platform values

| Value        | Sources searched |
|-------------|-----------------|
| `all` (default) | LinkedIn + Greenhouse + Lever + YC + HN + Indeed |
| `linkedin`  | LinkedIn Guest API only |
| `greenhouse`| Greenhouse public board API |
| `lever`     | Lever public postings API |
| `yc`        | workatastartup.com |
| `hn`        | HN "Who is Hiring?" thread (Algolia search) |
| `indeed`    | Indeed via SearXNG + scrape |
| `ats`       | Greenhouse + Lever |
| `startup`   | YC + HN + Greenhouse + Lever |

#### Output

```json
{
  "query": "golang developer remote",
  "jobs": [
    {
      "title": "Senior Go Engineer",
      "company": "Acme Corp",
      "url": "https://jobs.lever.co/acme/abc123",
      "job_id": "abc123",
      "source": "lever",
      "location": "Remote",
      "salary": "$140,000 - $180,000 USD",
      "job_type": "full-time",
      "remote": "remote",
      "experience": "mid-senior",
      "skills": ["Go", "Kubernetes", "PostgreSQL"],
      "description": "We are looking for...",
      "posted": "2026-02-15"
    }
  ],
  "summary": "Found 8 relevant Go developer positions..."
}
```

#### Sources detail

| Source | Method | Auth | Notes |
|--------|--------|------|-------|
| **LinkedIn** | Guest API (`/jobs-guest/jobs/api/`) | None | TLS fingerprint spoofing via BrowserClient; falls back to standard HTTP |
| **Greenhouse** | Public board API (`boards-api.greenhouse.io/v1/boards/{slug}/jobs`) | None | Discovers company slugs via SearXNG, then hits API in parallel |
| **Lever** | Public postings API (`api.lever.co/v0/postings/{slug}`) | None | Same slug-discovery pattern as Greenhouse |
| **YC** | SearXNG `site:workatastartup.com` + direct page scrape | None | Direct scrape requires BrowserClient |
| **HN** | Algolia HN search within "Who is Hiring?" thread | None | Thread ID cached 6h; falls back to Firebase parallel fetch |
| **Indeed** | SearXNG `site:indeed.com/viewjob` + page scrape | None | JSON-LD extraction for structured data |

---

### `remote_work_search`

Search for remote-first job listings on RemoteOK, WeWorkRemotely, and via SearXNG.

#### Input

| Parameter  | Type   | Required | Description |
|-----------|--------|----------|-------------|
| `query`   | string | ✅       | Search keywords (e.g. `golang`, `react developer`, `devops`) |
| `language`| string | —        | Answer language code (default: `all`) |

#### Output

```json
{
  "query": "golang devops",
  "jobs": [
    {
      "title": "DevOps Engineer (Go)",
      "company": "Remote Inc",
      "url": "https://remoteok.com/remote-jobs/golang-devops-123",
      "source": "remoteok",
      "salary": "$90000 - $130000",
      "location": "Worldwide",
      "tags": ["golang", "devops", "kubernetes"],
      "posted": "2026-02-18",
      "job_type": "remote"
    }
  ],
  "summary": "Found 6 remote DevOps/Go positions..."
}
```

#### Sources detail

| Source | Method | Notes |
|--------|--------|-------|
| **RemoteOK** | JSON API (`remoteok.com/api?tag=...`) | Filters by first significant keyword; keyword filter applied post-fetch |
| **WeWorkRemotely** | RSS feed (`weworkremotely.com/remote-jobs.rss`) | Full feed parsed, keyword-filtered client-side |
| **SearXNG** | `query + "remote job"` via Google + Bing engines | Parallel queries |

---

### `freelance_search`

Search for freelance projects on Upwork and Freelancer.com.

#### Input

| Parameter  | Type   | Required | Description |
|-----------|--------|----------|-------------|
| `query`   | string | ✅       | Search query (e.g. `golang API developer`, `React frontend`) |
| `platform`| string | —        | `upwork` \| `freelancer` \| `all` (default: `all`) |
| `language`| string | —        | Answer language code (default: `all`) |

#### Output

```json
{
  "query": "golang API developer",
  "projects": [
    {
      "title": "Build REST API in Go",
      "url": "https://www.upwork.com/freelance-jobs/apply/...",
      "platform": "upwork",
      "budget": "$500 - $1500 fixed",
      "skills": ["Go", "REST API", "PostgreSQL"],
      "description": "We need a Go developer to build...",
      "posted": "2026-02-19",
      "client_info": "4.8★ 23 reviews"
    }
  ],
  "summary": "Found 5 relevant Go API projects..."
}
```

#### Sources detail

| Source | Method | Notes |
|--------|--------|-------|
| **Freelancer.com** | Direct REST API (`api.freelancer.com/projects/0.1/projects/active`) | Rich data: budgets, bid counts, skills; no auth required |
| **Upwork** | SearXNG `site:upwork.com/freelance-jobs/apply` via Google + Bing | URL-filtered to job postings only |

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
│   │   ├── jobs/                    # Job source implementations
│   │   │   ├── linkedin.go          # LinkedIn Guest API + JSON-LD detail fetch
│   │   │   ├── ats.go               # Greenhouse + Lever public APIs
│   │   │   ├── hnjobs.go            # HN "Who is Hiring?" via Algolia + Firebase
│   │   │   ├── ycjobs.go            # YC workatastartup.com
│   │   │   ├── remotejobs.go        # RemoteOK API + WeWorkRemotely RSS
│   │   │   └── indeed.go            # Indeed via SearXNG + JSON-LD scrape
│   │   └── sources/
│   │       └── freelancer.go        # Freelancer.com REST API
│   ├── jobserver/
│   │   └── register.go              # MCP tool registrations (3 tools)
│   └── toolutil/
│       └── toolutil.go              # Shared helpers: cache, fetch, lang
├── docs/
│   ├── README.md                    # This file
│   └── compare.md                   # Comparison with competitors
└── deploy/
    └── go_job.service               # systemd unit
```

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
| `GITHUB_TOKEN` | — | GitHub token (unused in go_job, reserved) |

## Caching

Results are cached at two levels:

- **L1 (in-memory):** `sync.Map` with TTL eviction. Fast, lost on restart.
- **L2 (Redis):** Optional. Survives restarts, shared across instances.

Cache key format: `sha256(tool_name + "|" + param1 + "|" + param2 + ...)`.

## Rate Limiting & Anti-bot

- **LinkedIn:** Uses `bogdanfinn/tls-client` for Chrome TLS fingerprint when `BrowserClient` is configured. Falls back to standard `net/http` with Chrome User-Agent.
- **HN Firebase:** Max 10 concurrent requests, staggered delays per batch.
- **LinkedIn detail fetch:** Staggered 1s delays between parallel fetches.
- **All sources:** HTTP retry with exponential backoff (`engine.RetryHTTP`).
