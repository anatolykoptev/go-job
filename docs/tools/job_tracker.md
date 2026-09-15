# Tool: `job_tracker`

> **Category:** Tracker | **Source:** `internal/engine/jobs/tracker.go`
>
> Replaces the retired `job_tracker_add` / `job_tracker_list` / `job_tracker_update` tools.
> Select the operation with `action`.

Track job applications through the pipeline. Backed by Postgres `hunt_jobs` +
`hunt_ratings` — see [ADR-002](../adr/ADR-go-job-002-retire-sqlite-tracker.md). The local
SQLite `tracker.db` was retired; it survives only as the input to the one-shot
`cmd/migrate-tracker`.

This tool is one of the two places the product's longitudinal record accumulates (the other
is the hunt ingest). That record is the reason a ranking decision here can be better than a
chat window's — see [principles.md §4](../architecture/principles.md#4-the-moat-ranked-by-durability).

---

## Input

| Parameter | Type | Required | Description |
|---|---|---|---|
| `action` | string | ✅ | `add` \| `list` \| `update` |
| `title` | string | `add` | Job title |
| `company` | string | `add` | Company name |
| `id` | int | `update` | Job ID returned by `add` or `list` |
| `url` | string | — | Job posting URL |
| `status` | string | — | `saved` (default on `add`) \| `applied` \| `interview` \| `offer` \| `rejected`. On `list`, filters; empty = all |
| `notes` | string | — | Free-form notes. On `update`, **replaces** the existing notes |
| `salary` | string | — | Salary range if known (e.g. `$180k-$220k`) |
| `location` | string | — | Job location |
| `limit` | int | — | `list` only. Default 50, max 100 |

`action=update` requires at least one of `status` or `notes`.

---

## Output

`add` / `update`:

```json
{"id": 42, "message": "Job 'Senior Go Developer' at 'Stripe' saved with status 'applied' (id=42)"}
```

`list`:

```json
{
  "jobs": [
    {
      "id": 42,
      "title": "Senior Go Developer",
      "company": "Stripe",
      "url": "https://stripe.com/jobs/123",
      "status": "applied",
      "notes": "Applied via LinkedIn. Interview scheduled for March 1.",
      "salary": "$180k-$220k",
      "location": "Remote",
      "created_at": "2026-02-19T20:45:00Z",
      "updated_at": "2026-02-20T10:00:00Z"
    }
  ],
  "total": 1
}
```

---

## Status values

| Status | Meaning |
|---|---|
| `saved` | Bookmarked, not yet applied (default) |
| `applied` | Application submitted |
| `interview` | In interview process |
| `offer` | Received an offer |
| `rejected` | Rejected or withdrawn |

```
saved → applied → interview → offer
                            ↘ rejected
```

Any transition is allowed — backwards and skipped stages included.

---

## Mapping onto the hunt tables

The tracker exposes a 5-stage subset of the 9-stage hunt vocabulary. `status` maps onto
`hunt_ratings.stage`; `id` is `hunt_jobs.id`; `created_at` is `hunt_jobs.first_seen_at`.
Full column mapping in [ADR-002](../adr/ADR-go-job-002-retire-sqlite-tracker.md).

Two axes exist on `hunt_ratings` — `triage` and `stage` — and they are independent
([ADR-003](../adr/ADR-go-job-003.md)). The tracker writes only the pipeline axis; a star
in the admin UI writes only the triage axis. Writing one never clears the other.

`fit_score` is **never** written by tracker operations — it is computed asynchronously by
the hunt engine.

---

## Notes

- Requires `DATABASE_URL`.
- IDs are `hunt_jobs.id` (bigserial). SQLite row IDs from before the migration are obsolete.
- Not cached — reads and writes hit Postgres directly.

---

## Implementation

- **File:** `internal/engine/jobs/tracker.go`
- **Registration:** `internal/jobserver/tool_tracker.go`
