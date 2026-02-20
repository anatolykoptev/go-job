# Tool: `remote_work_search`

> **Category:** Search | **Source:** `internal/engine/jobs/remotejobs.go`

Search for remote-first job listings on RemoteOK, WeWorkRemotely, Remotive, and via SearXNG.

---

## Input

| Parameter  | Type   | Required | Description |
|-----------|--------|----------|-------------|
| `query`   | string | ✅       | Search keywords (e.g. `golang`, `react developer`, `devops`) |
| `language`| string | —        | Answer language code (default: `all`) |

---

## Output

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

---

## Sources

| Source | Method | Notes |
|--------|--------|-------|
| **RemoteOK** | JSON API (`remoteok.com/api?tag=...`) | Filters by first significant keyword; AND-logic keyword filter applied post-fetch with OR fallback |
| **WeWorkRemotely** | RSS feed (`weworkremotely.com/remote-jobs.rss`) | Full feed parsed, keyword-filtered client-side |
| **Remotive** | JSON API (`remotive.com/api/remote-jobs?search=...`) | Free public API, server-side search filter, no auth |
| **SearXNG** | `query + "remote job"` via Google + Bing engines | Parallel queries |

---

## Filtering Logic

RemoteOK tags are extracted from job metadata with stop-word filtering (`engineer`, `developer`, `senior`, etc. are skipped to find meaningful tech tags). Keyword matching uses **AND logic** (all keywords must match) with **OR fallback** if AND yields no results.

---

## Caching

Results cached for **15 min** (L1 in-memory + L2 Redis if configured).
Cache key: `sha256("remote_work_search|" + query)`.

---

## Implementation

- **File:** `internal/engine/jobs/remotejobs.go`
- **Registration:** `internal/jobserver/register.go`
