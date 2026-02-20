# Tool: `job_tracker_add`

> **Category:** Tracker | **Source:** `internal/engine/jobs/tracker.go`

Save a job to the local SQLite tracker (`~/.go_job/tracker.db`). The DB is created automatically on first use.

---

## Input

| Parameter  | Type   | Required | Description |
|-----------|--------|----------|-------------|
| `title`   | string | ✅       | Job title |
| `company` | string | ✅       | Company name |
| `url`     | string | —        | Job posting URL |
| `status`  | string | —        | `saved` (default) \| `applied` \| `interview` \| `offer` \| `rejected` |
| `notes`   | string | —        | Free-form notes (recruiter name, salary discussed, next steps, etc.) |
| `salary`  | string | —        | Salary range if known (e.g. `$180k-$220k`, `300 000 ₽`) |
| `location`| string | —        | Job location (e.g. `Remote`, `Berlin`, `Москва`) |

---

## Output

```json
{"id": 42, "message": "Job 'Senior Go Developer' at 'Stripe' saved with status 'applied' (id=42)"}
```

### Fields

| Field | Type | Description |
|-------|------|-------------|
| `id` | int | Auto-incremented ID for use with `job_tracker_update` |
| `message` | string | Confirmation message |

---

## Status values

| Status | Meaning |
|--------|---------|
| `saved` | Bookmarked, not yet applied (default) |
| `applied` | Application submitted |
| `interview` | In interview process |
| `offer` | Received an offer |
| `rejected` | Rejected or withdrawn |

---

## Application Pipeline

```
saved → applied → interview → offer
                            ↘ rejected
```

---

## Notes

- DB file: `~/.go_job/tracker.db` — created automatically, persists across server restarts.
- `title` and `company` are required; all other fields are optional.
- `status` defaults to `saved` if omitted or empty.
- Invalid `status` values return an error.
- **Not cached** — writes directly to SQLite.

---

## Typical Workflow

```
job_search → find interesting jobs
job_tracker_add (status=saved) → bookmark them
job_tracker_add (status=applied) → after applying
job_tracker_update → move through pipeline
job_tracker_list → review current status
```

---

## Implementation

- **File:** `internal/engine/jobs/tracker.go` — `AddTrackedJob()`
- **DB:** SQLite via `modernc.org/sqlite` (pure Go, no CGO)
- **Registration:** `internal/jobserver/register.go`
- **Tests:** `internal/engine/jobs/tracker_test.go`
