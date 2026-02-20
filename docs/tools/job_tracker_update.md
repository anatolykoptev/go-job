# Tool: `job_tracker_update`

> **Category:** Tracker | **Source:** `internal/engine/jobs/tracker.go`

Update the status and/or notes for a tracked job by its ID. At least one of `status` or `notes` must be provided.

---

## Input

| Parameter | Type   | Required | Description |
|----------|--------|----------|-------------|
| `id`     | int    | ✅       | Job ID from `job_tracker_add` or `job_tracker_list` |
| `status` | string | —        | New status: `saved` \| `applied` \| `interview` \| `offer` \| `rejected` |
| `notes`  | string | —        | Updated notes (replaces existing notes) |

At least one of `status` or `notes` must be provided.

---

## Output

```json
{"id": 42, "message": "Job #42 updated successfully"}
```

---

## Status Pipeline

```
saved → applied → interview → offer
                            ↘ rejected
```

Any transition is allowed — you can move backwards or skip stages.

---

## Notes

- `id=0` returns an error.
- Calling with neither `status` nor `notes` returns an error.
- Invalid `status` values return an error.
- `notes` replaces the existing notes field entirely (not appended).
- `updated_at` is set to current UTC time on every update.
- **Not cached** — writes directly to SQLite.

---

## Typical Workflow

```
# After submitting application
job_tracker_update (id=42, status=applied, notes="Applied 2026-02-20 via LinkedIn")

# After getting interview invite
job_tracker_update (id=42, status=interview, notes="Technical interview March 1, 14:00 UTC. Interviewer: John Doe")

# After receiving offer
job_tracker_update (id=42, status=offer, notes="Offer: $195k base + equity. Deadline March 10.")

# If rejected
job_tracker_update (id=42, status=rejected, notes="Rejected after final round. Feedback: need more distributed systems experience.")
```

---

## Implementation

- **File:** `internal/engine/jobs/tracker.go` — `UpdateTrackedJob()`
- **DB:** `~/.go_job/tracker.db` (SQLite)
- **Registration:** `internal/jobserver/register.go`
- **Tests:** `internal/engine/jobs/tracker_test.go`
