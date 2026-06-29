# ADR-003: Two-plane job model → two-axis rating split (addendum migration 012)

Date: 2026-06-28 (original) / 2026-06-29 (addendum)
Status: Accepted (addendum supersedes "no column split" decision)

## Context

`hunt_jobs.status` = posting lifecycle (open/closed/merged/archived/ended). Operator-owned.

`hunt_ratings.stage` = originally a single-funnel column:
triage (new→interesting→saved→discarded→claimed) → pipeline (applied→interview→offer→rejected).

The original ADR (2026-06-28) decided against splitting this into two columns ("a 1×1
self-tracker doesn't need a two-column design"). Operator workflow revealed the single-column
model was a blocking limitation: the operator needed to hold e.g. "saved" AND "applied"
simultaneously — impossible with one enum column.

## Decision (addendum, migration 012)

Split `hunt_ratings.stage` into two independent axes:

| Column    | Axis    | Valid values                              | '' meaning   |
|-----------|---------|-------------------------------------------|--------------|
| `triage`  | triage  | interesting, saved, discarded             | untriaged    |
| `stage`   | pipeline| claimed, applied, interview, offer, rejected | not in pipeline |

Key design choices:
1. **Both axes are independent**: writing to one never clears the other. `Rate()` uses
   CASE guards: `CASE WHEN EXCLUDED.triage = '' THEN hunt_ratings.triage ELSE EXCLUDED.triage END`.
2. **'new' is retired**: old rows are migrated to both-empty. No new rows use 'new'.
3. **'claimed' moved to pipeline**: it represents "I accepted this role for an interview",
   which is a pipeline position, not an interest signal.
4. **Star controls triage only**: star-on → triage='saved', star-off → triage=''.
   Pipeline stages are never touched by a star click.
5. **Separate forms**: the job-detail page has a `/triage` form (triage-only) and a
   `/rate` form (pipeline-only). The jobs-table inline dropdown controls only the pipeline axis.
6. **Shortlist two-axis WHERE**: `(r.triage = ANY($N) OR r.stage = ANY($N+1))`.
7. **Domain primitives**: `hunt.TriageStages`, `hunt.PipelineStages`, `hunt.AllStages`,
   `hunt.StarSoftTriageValues` — all authoritative; UI derives from these.

## Migration (012_split_triage_pipeline.sql)

Idempotent:
- `ALTER TABLE hunt_ratings ADD COLUMN IF NOT EXISTS triage TEXT NOT NULL DEFAULT ''`
- `ALTER TABLE hunt_ratings ALTER COLUMN stage SET DEFAULT ''`
- Backfill: triage-axis values (interesting/saved/discarded) → move to triage column
- Backfill: 'new' → both-empty
- Index on `(triage, updated_at DESC)`

## Consequences

- Operator can hold e.g. triage='saved' AND stage='applied' simultaneously.
- The two `//nolint` comment in `stage_optgroup.go` (`stageOptgroupHTML`) remains for
  legacy/audit-trail use; the jobs-table dropdown uses `pipelineOptgroupHTML`.
- Any future triage value: edit `hunt.TriageStages`. Any future pipeline stage: edit
  `hunt.PipelineStages`. Both axes derive automatically.
- Migration 012 is idempotent; safe to replay.
