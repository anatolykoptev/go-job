# ADR-003: Two-plane job model

Date: 2026-06-28
Status: Accepted

## Context

`hunt_jobs.status` = posting lifecycle (open/closed/merged/archived/ended). Operator-owned; `SetStatus` is the sole off-`open` writer; no enricher bumps status for jobs.

`hunt_ratings.stage` = operator's one-funnel: triage (new→interesting→saved→discarded→claimed) → pipeline (applied→interview→offer→rejected). A single sequential dimension, not two separate columns.

The operator UI previously showed two dropdowns with similar labels and the status filter matched zero rows for pipeline values.

## Decision

1. **No column split**: `hunt_ratings.stage` stays as one column. A 1×1 self-tracker doesn't need a two-column design; expand-contract escape hatch available if needed.
2. **Domain grouping primitive**: `hunt.TriageStages` and `hunt.PipelineStages` slices own the authoritative group membership; `AllStages` is their concatenation.
3. **Renderer dedup**: `stageOptgroupHTML` in `internal/adminui` is the single renderer for stage selects; both the jobs-table dropdown and the detail rate-form derive from it.
4. **Status filter restricted** to `hunt.AllStatuses` — no cross-plane values.

## Consequences

- Operator sees distinct "Posting status" and "My pipeline" labels; no apparent duplicates.
- Any future stage addition edits `TriageStages` or `PipelineStages` — both renderers update automatically.
- Status filter returns correct row counts.
