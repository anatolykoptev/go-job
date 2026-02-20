# Tool: `job_search`

> **Category:** Search | **Source:** `internal/engine/jobs/` (linkedin, ats, hnjobs, ycjobs, indeed, habr)

Search for job listings across 10+ sources: LinkedIn, Greenhouse, Lever, YC, HN, Indeed, Хабр Карьера.

---

## Input

| Parameter    | Type   | Required | Description |
|-------------|--------|----------|-------------|
| `query`     | string | ✅       | Job search keywords (e.g. `golang developer`, `data engineer`) |
| `location`  | string | —        | City, country, or `Remote` (e.g. `Berlin`, `United States`). 42 locations map to LinkedIn geoId for precise filtering. |
| `experience`| string | —        | `internship` \| `entry` \| `associate` \| `mid-senior` \| `director` \| `executive` |
| `job_type`  | string | —        | `full-time` \| `part-time` \| `contract` \| `temporary` |
| `remote`    | string | —        | `onsite` \| `hybrid` \| `remote` |
| `time_range`| string | —        | `day` \| `week` \| `month` |
| `salary`    | string | —        | Salary filter for LinkedIn: `40k+` \| `60k+` \| `80k+` \| `100k+` \| `120k+` \| `140k+` \| `160k+` \| `180k+` \| `200k+` |
| `easy_apply`| bool   | —        | LinkedIn only: filter to Easy Apply jobs (`true`) |
| `platform`  | string | —        | Source filter — see table below |
| `language`  | string | —        | Answer language code (default: `all`) |

### Platform values

| Value           | Sources searched |
|----------------|-----------------|
| `all` (default) | LinkedIn + Greenhouse + Lever + YC + HN + Indeed + Хабр |
| `linkedin`      | LinkedIn Guest API only |
| `greenhouse`    | Greenhouse public board API |
| `lever`         | Lever public postings API |
| `yc`            | workatastartup.com |
| `hn`            | HN "Who is Hiring?" thread (Algolia search) |
| `indeed`        | Indeed via SearXNG + scrape |
| `habr`          | Хабр Карьера public JSON API |
| `ats`           | Greenhouse + Lever |
| `startup`       | YC + HN + Greenhouse + Lever |

---

## Output

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
      "salary": "$140k–180k USD/yr",
      "salary_min": 140000,
      "salary_max": 180000,
      "salary_currency": "USD",
      "salary_interval": "year",
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

---

## Sources

| Source | Method | Auth | Notes |
|--------|--------|------|-------|
| **LinkedIn** | Guest API (`/jobs-guest/jobs/api/`) | None | TLS fingerprint spoofing via BrowserClient; falls back to standard HTTP |
| **Greenhouse** | Public board API (`boards-api.greenhouse.io/v1/boards/{slug}/jobs`) | None | Discovers company slugs via SearXNG, then hits API in parallel |
| **Lever** | Public postings API (`api.lever.co/v0/postings/{slug}`) | None | Same slug-discovery pattern as Greenhouse |
| **YC** | SearXNG `site:workatastartup.com` + direct page scrape | None | Direct scrape requires BrowserClient |
| **HN** | Algolia HN search within "Who is Hiring?" thread | None | Thread ID cached 6h; falls back to Firebase parallel fetch |
| **Indeed** | Internal iOS GraphQL API (`apis.indeed.com/graphql`) | `INDEED_API_KEY` env | Direct GraphQL with salary ranges; SearXNG fallback if API fails |
| **Хабр Карьера** | Public JSON API (`career.habr.com/api/frontend/vacancies`) | None | Salary, skills, location, remote flag, employment type |

---

## Caching

Results cached for **15 min** (L1 in-memory + L2 Redis if configured).
Cache key: `sha256("job_search|" + query + "|" + location + "|" + platform + ...)`.

---

## Implementation

- **File:** `internal/engine/jobs/linkedin.go`, `ats.go`, `hnjobs.go`, `ycjobs.go`, `indeed.go`, `habr.go`
- **Registration:** `internal/jobserver/register.go`
- **Parallel fetch:** all sources run concurrently via goroutines + `sync.WaitGroup`
- **Rate limiting:** staggered 1s delays between LinkedIn detail fetches; max 10 concurrent HN Firebase requests
- **Retry:** `engine.RetryHTTP` with exponential backoff on all HTTP calls
