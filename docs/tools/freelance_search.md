# Tool: `freelance_search`

> **Category:** Search | **Source:** `internal/engine/sources/freelancer.go`

Search for freelance projects on Upwork and Freelancer.com.

---

## Input

| Parameter  | Type   | Required | Description |
|-----------|--------|----------|-------------|
| `query`   | string | ✅       | Search query (e.g. `golang API developer`, `React frontend`) |
| `platform`| string | —        | `upwork` \| `freelancer` \| `all` (default: `all`) |
| `language`| string | —        | Answer language code (default: `all`) |

---

## Output

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

---

## Sources

| Source | Method | Notes |
|--------|--------|-------|
| **Freelancer.com** | Direct REST API (`api.freelancer.com/projects/0.1/projects/active`) | Rich data: budgets, bid counts, skills; no auth required |
| **Upwork** | SearXNG `site:upwork.com/freelance-jobs/apply` via Google + Bing | URL-filtered to job postings only |

---

## Caching

Results cached for **15 min** (L1 in-memory + L2 Redis if configured).
Cache key: `sha256("freelance_search|" + query + "|" + platform)`.

---

## Implementation

- **File:** `internal/engine/sources/freelancer.go`
- **Registration:** `internal/jobserver/register.go`
