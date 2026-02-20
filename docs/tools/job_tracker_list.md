# Tool: `job_tracker_list`

> **Category:** Tracker | **Source:** `internal/engine/jobs/tracker.go`

List tracked jobs from the local SQLite tracker, optionally filtered by status. Returns jobs sorted by most recently updated first.

---

## Input

| Parameter | Type   | Required | Description |
|----------|--------|----------|-------------|
| `status` | string | —        | Filter by status: `saved` \| `applied` \| `interview` \| `offer` \| `rejected` (empty = all) |
| `limit`  | int    | —        | Max results to return (default: `50`, max: `100`) |

---

## Output

```json
{
  "jobs": [
    {
      "id": 42,
      "title": "Senior Go Developer",
      "company": "Stripe",
      "url": "https://stripe.com/jobs/123",
      "status": "applied",
      "notes": "Applied via LinkedIn. Recruiter: Jane Smith. Interview scheduled for March 1.",
      "salary": "$180k-$220k",
      "location": "Remote",
      "created_at": "2026-02-19T20:45:00Z",
      "updated_at": "2026-02-20T10:00:00Z"
    }
  ],
  "total": 1
}
```

### Fields

| Field | Type | Description |
|-------|------|-------------|
| `jobs` | []Job | List of tracked jobs |
| `total` | int | Total count matching the filter |
| `jobs[].id` | int | Job ID (use with `job_tracker_update`) |
| `jobs[].title` | string | Job title |
| `jobs[].company` | string | Company name |
| `jobs[].url` | string | Job posting URL |
| `jobs[].status` | string | Current pipeline status |
| `jobs[].notes` | string | Free-form notes |
| `jobs[].salary` | string | Salary range |
| `jobs[].location` | string | Job location |
| `jobs[].created_at` | string | ISO 8601 timestamp when added |
| `jobs[].updated_at` | string | ISO 8601 timestamp of last update |

---

## Notes

- `limit=0` defaults to `50`.
- Results are ordered by `updated_at DESC` (most recently changed first).
- **Not cached** — reads directly from SQLite.

---

## Typical Workflow

```
job_tracker_list                    → see all tracked jobs
job_tracker_list (status=applied)   → see what's in flight
job_tracker_list (status=interview) → prepare for upcoming interviews
job_tracker_update (id=42, status=offer) → move to next stage
```

---

## Implementation

- **File:** `internal/engine/jobs/tracker.go` — `ListTrackedJobs()`
- **DB:** `~/.go_job/tracker.db` (SQLite)
- **Registration:** `internal/jobserver/register.go`
- **Tests:** `internal/engine/jobs/tracker_test.go`
