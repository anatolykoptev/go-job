# go_job MCP — Documentation

**go_job** is a standalone Go MCP server exposing **11 tools** for job search, resume optimization, career research, and application tracking.

- **MCP endpoint:** `http://localhost:8891/mcp`
- **Health:** `http://localhost:8891/health`
- **Metrics:** `http://localhost:8891/metrics`
- **Transport:** HTTP (Streamable) or `--stdio`

---

## Tools Overview

| Tool | Category | Description |
|------|----------|-------------|
| [`job_search`](#job_search) | Search | LinkedIn, Greenhouse, Lever, YC, HN, Indeed, Хабр (10+ sources) |
| [`remote_work_search`](#remote_work_search) | Search | RemoteOK, WeWorkRemotely, SearXNG |
| [`freelance_search`](#freelance_search) | Search | Upwork, Freelancer.com |
| [`resume_analyze`](#resume_analyze) | Resume | ATS score (0-100), missing keywords, gaps, recommendations |
| [`cover_letter_generate`](#cover_letter_generate) | Resume | Tailored cover letter (3 tones) |
| [`resume_tailor`](#resume_tailor) | Resume | Rewrite resume sections to match JD |
| [`salary_research`](#salary_research) | Research | p25/median/p75 salary benchmarks |
| [`company_research`](#company_research) | Research | Size, funding, tech stack, culture, news |
| [`job_tracker_add`](#job_tracker_add) | Tracker | Save job to local SQLite tracker |
| [`job_tracker_list`](#job_tracker_list) | Tracker | List tracked jobs by status |
| [`job_tracker_update`](#job_tracker_update) | Tracker | Update status/notes by ID |

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

### `resume_analyze`

Analyze a resume against a job description. Returns ATS compatibility score, keyword analysis, and actionable recommendations.

#### Input

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `resume_text` | string | ✅ | Resume as plain text (paste directly, no PDF) |
| `job_description` | string | ✅ | Full job description text |

#### Output

```json
{
  "ats_score": 72,
  "matching_keywords": ["Go", "REST API", "PostgreSQL"],
  "missing_keywords": ["gRPC", "Kubernetes", "Prometheus"],
  "gaps": ["No distributed systems experience at scale", "Missing cloud certifications"],
  "recommendations": [
    "Add gRPC project to portfolio section",
    "Mention Prometheus monitoring in experience bullets",
    "Add Kubernetes deployment experience"
  ],
  "summary": "Strong backend match but missing cloud/container keywords. ATS score can be improved to 85+ by adding Kubernetes and gRPC."
}
```

---

### `cover_letter_generate`

Generate a tailored cover letter from resume and job description.

#### Input

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `resume_text` | string | ✅ | Resume as plain text |
| `job_description` | string | ✅ | Job description text |
| `tone` | string | — | `professional` (default) \| `friendly` \| `concise` |

#### Output

```json
{
  "cover_letter": "Dear Hiring Manager,\n\nI am excited to apply for the Senior Go Engineer position at Stripe...",
  "word_count": 287,
  "tone": "professional"
}
```

---

### `resume_tailor`

Rewrite resume sections to better match a specific job description. Returns tailored sections, keyword diff, and complete tailored resume.

#### Input

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `resume_text` | string | ✅ | Resume as plain text |
| `job_description` | string | ✅ | Job description to tailor for |

#### Output

```json
{
  "tailored_sections": {
    "Summary": "Results-driven Go engineer with 5+ years building distributed systems...",
    "Skills": "Go, gRPC, Kubernetes, Prometheus, PostgreSQL, Terraform"
  },
  "added_keywords": ["gRPC", "Prometheus", "distributed systems"],
  "removed_keywords": ["PHP", "jQuery"],
  "diff_summary": "Added gRPC and Prometheus to skills, reordered experience bullets to highlight distributed systems work, removed legacy frontend stack.",
  "tailored_resume": "John Doe\nSenior Go Engineer\n..."
}
```

---

### `salary_research`

Research salary ranges for a role and location. Aggregates data from levels.fyi, Glassdoor, hh.ru, Хабр Карьера via SearXNG + LLM synthesis.

#### Input

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `role` | string | ✅ | Job title (e.g. `Senior Go Developer`, `Data Engineer`) |
| `location` | string | — | City/country/region (e.g. `San Francisco`, `Remote`, `Москва`) |
| `experience` | string | — | `junior` \| `mid` \| `senior` \| `lead` |

#### Output

```json
{
  "role": "Senior Go Developer",
  "location": "San Francisco",
  "currency": "USD",
  "p25": 160000,
  "median": 195000,
  "p75": 230000,
  "sources": ["levels.fyi", "glassdoor.com", "linkedin.com"],
  "notes": "Equity not included. Varies significantly by company tier (FAANG vs startup).",
  "updated_at": "2025"
}
```

**Note:** For Russian locations (Москва, Россия, etc.) uses hh.ru and Хабр Карьера as primary sources and returns RUB.

---

### `company_research`

Research a company before applying or interviewing. Returns size, funding, tech stack, culture notes, Glassdoor rating, and recent news.

#### Input

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `company_name` | string | ✅ | Company name (e.g. `Google`, `Яндекс`, `Stripe`) |

#### Output

```json
{
  "name": "Stripe",
  "size": "8000-10000",
  "founded": "2010",
  "industry": "FinTech / Payments",
  "funding": "Private, $95B valuation",
  "tech_stack": ["Ruby", "Go", "Java", "React", "AWS", "Kafka"],
  "culture_notes": "High-performance engineering culture. Strong emphasis on writing and documentation. Remote-friendly with offices in SF, NYC, Dublin.",
  "recent_news": [
    "Launched Stripe Tax globally in 2024",
    "Expanded to 50 new countries"
  ],
  "glassdoor_rating": 4.1,
  "website": "https://stripe.com",
  "summary": "Stripe is a leading global payments infrastructure company..."
}
```

---

### `job_tracker_add`

Save a job to the local SQLite tracker (`~/.go_job/tracker.db`).

#### Input

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `title` | string | ✅ | Job title |
| `company` | string | ✅ | Company name |
| `url` | string | — | Job posting URL |
| `status` | string | — | `saved` (default) \| `applied` \| `interview` \| `offer` \| `rejected` |
| `notes` | string | — | Any notes (recruiter name, salary discussed, etc.) |
| `salary` | string | — | Salary range if known |
| `location` | string | — | Job location |

#### Output

```json
{"id": 42, "message": "Job 'Senior Go Developer' at 'Stripe' saved with status 'applied' (id=42)"}
```

---

### `job_tracker_list`

List tracked jobs, optionally filtered by status.

#### Input

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `status` | string | — | Filter: `saved` \| `applied` \| `interview` \| `offer` \| `rejected` (empty = all) |
| `limit` | int | — | Max results (default: 50, max: 100) |

#### Output

```json
{
  "jobs": [
    {
      "id": 42,
      "title": "Senior Go Developer",
      "company": "Stripe",
      "url": "https://stripe.com/jobs/123",
      "status": "applied",
      "notes": "Applied via LinkedIn. Recruiter: Jane Smith",
      "salary": "$180k-$220k",
      "location": "Remote",
      "created_at": "2026-02-19T20:45:00Z",
      "updated_at": "2026-02-19T20:45:00Z"
    }
  ],
  "total": 1
}
```

---

### `job_tracker_update`

Update status and/or notes for a tracked job by ID.

#### Input

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `id` | int | ✅ | Job ID from `job_tracker_add` or `job_tracker_list` |
| `status` | string | — | New status: `saved` \| `applied` \| `interview` \| `offer` \| `rejected` |
| `notes` | string | — | Updated notes |

At least one of `status` or `notes` must be provided.

#### Output

```json
{"id": 42, "message": "Job #42 updated successfully"}
```

#### Application Pipeline

```
saved → applied → interview → offer
                            ↘ rejected
```

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
│   ├── README.md                    # This file
│   ├── compare.md                   # Comparison with competitors
│   └── roadmap.md                   # Feature roadmap
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
