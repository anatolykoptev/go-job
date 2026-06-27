# ADR-go-job-002: Retire SQLite tracker.db, unify into postgres hunt tables

**Status:** Accepted
**Date:** 2026-06-26
**Author:** Anatoly Koptev

## Context

The `job_tracker` MCP tool set (`job_tracker add/list/update`) stored data in a local SQLite file (`tracker.db`). The hunt opportunity pipeline (Phase 1) introduced `hunt_jobs` and `hunt_ratings` tables in postgres with the same semantic domain. Two stores for the same domain caused:

- Duplicate "saved" jobs with divergent IDs between SQLite and postgres.
- `fit_score` computed against hunt rows, but tracker rows had no link.
- No single "all tracked jobs" query across both stores.
- SQLite file not backed up by the postgres backup strategy.

## Decision

Re-point all three `job_tracker` MCP tools to read/write postgres (`hunt_jobs` + `hunt_ratings`) exclusively. Remove SQLite machinery from the runtime service.

### Column mapping

| TrackedJob field | Source |
|---|---|
| `id` | `hunt_jobs.id` |
| `title` | `hunt_jobs.title` |
| `company` | `hunt_jobs.company` |
| `url` | `hunt_jobs.url` |
| `status` | `hunt_ratings.stage` (tracker uses 5-stage subset) |
| `notes` | `hunt_ratings.note` (salary appended inline when not structured) |
| `salary` | Formatted from `hunt_jobs.salary_min/max/currency/interval`; or note prefix |
| `location` | `hunt_jobs.location` |
| `created_at` | `hunt_jobs.first_seen_at` |
| `updated_at` | `hunt_ratings.updated_at` |

### Stage vocabulary (tracker subset)

The tracker uses 5 stages from the 9-stage hunt vocabulary:

| TrackedJob.Status | hunt_ratings.stage |
|---|---|
| `saved` | `saved` |
| `applied` | `applied` |
| `interview` | `interview` |
| `offer` | `offer` |
| `rejected` | `rejected` |

### What is NOT written

- `fit_score` — never written by tracker operations (scores are computed async by the hunt engine)

### ID space

SQLite row IDs (1-based, sequential) are NOT preserved. Migration maps each SQLite row to a new `hunt_jobs.id` (bigserial). The old SQLite IDs are obsolete after migration.

### adminUser

The single-operator assumption: `user_name = "krolik"` for all tracker ratings. This is per the single-tenant deployment model.

## Migration

`cmd/migrate-tracker` reads the live `tracker.db` and upserts each row into postgres. Idempotent — safe to re-run. Does NOT write `fit_score`.

```bash
go run ./cmd/migrate-tracker -db ~/tracker.db -dsn "$DATABASE_URL"
```

## modernc.org/sqlite

The `modernc.org/sqlite` dependency remains in `go.mod` because `cmd/migrate-tracker` uses it to read the legacy SQLite file. It is NOT imported anywhere under `internal/`. After the migration is complete and `cmd/migrate-tracker` is no longer needed, the dependency can be removed with `go mod tidy`.

## Consequences

- Positive: Single source of truth for tracked jobs. `fit_score` linkage works naturally.
- Positive: SQLite file no longer needed for runtime operations.
- Positive: Postgres backup covers all job data.
- Neutral: SQLite IDs change after migration — any bookmarked IDs become invalid.
- Negative: Requires DATABASE_URL at runtime (was file-only before).
