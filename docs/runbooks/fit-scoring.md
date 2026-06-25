# Fit-Scoring Runbook

## Overview

go-job runs a hybrid Jaccard→LLM cascade scorer on every newly-ingested job
(phase 4–6). The scorer filters cheap rejections first (recency, keyword overlap)
and calls an LLM only for plausible matches. Scores are persisted to `hunt_jobs`
and consumed by the `hunt_list` / `job_match` MCP tools.

**Metrics endpoint:** `curl http://localhost:9891/metrics | grep hunt_fit\|hunt_score`

---

## Env-knob reference

| Variable | Default | Purpose |
|---|---|---|
| `HUNT_SCORE_ENABLED` | `true` | Master on/off switch for LLM scoring |
| `HUNT_NOTIFY_MIN_FIT` | `0` | Minimum `fit_score` to notify (0 = gate open, all scores pass) |
| `HUNT_SCORE_MIN_JACCARD` | `8` | Jaccard pre-filter threshold (0–100); below → `reject` without LLM |
| `HUNT_SCORE_MAX_LLM_PER_CYCLE` | `50` | Per-cycle LLM budget ceiling (circuit breaker) |
| `HUNT_SCORE_SWEEP_LIMIT` | `50` | Max unscored-open jobs to backfill per cycle |
| `HUNT_SCORE_FAIL_OPEN` | `true` | On LLM error: `true` → notify with degraded card; `false` → drop |
| `HUNT_NOTIFY_MAX_AGE` | `48h` | Recency gate; jobs posted older than this → `stale` without LLM |
| `HUNT_SCORE_RESCORE_ALL` | `false` | One-shot: sweep re-scores ALL open jobs, not just unscored ones |

All knobs are read **per-cycle** (no redeploy needed to change them at runtime).

---

## How to disable scoring

Set `HUNT_SCORE_ENABLED=false` and restart (or wait for next cycle if
the runtime-read pattern applies). Jobs continue to be ingested and notified
with a recency-only card (the `unscored` fail-open path). The `hunt_list` and
`job_match` tools still return jobs; `fit_score` will be NULL for new rows.

---

## How to re-open the fit gate

If `HUNT_NOTIFY_MIN_FIT` is set too high and jobs are being silently dropped:

```bash
# Check how many are being dropped
curl -s http://localhost:9891/metrics | grep 'hunt_notify_total{outcome="low_fit"}'

# Temporarily re-open the gate (all scored jobs notify)
HUNT_NOTIFY_MIN_FIT=0  # set in deploy env and restart, or re-export for current process
```

The metric `hunt_notify_total{outcome="low_fit"}` shows the drop rate. If it is
unexpectedly high, lower `HUNT_NOTIFY_MIN_FIT` or check the fit-score distribution:

```bash
curl -s http://localhost:9891/metrics | grep hunt_fit_score
```

---

## One-shot re-score (HUNT_SCORE_RESCORE_ALL)

To re-score all open jobs (e.g. after a profile update):

1. Set `HUNT_SCORE_RESCORE_ALL=true` in the deploy env.
2. Restart the service (or wait for the next cycle — the knob is read per-cycle).
3. The end-of-cycle sweep will score up to `HUNT_SCORE_SWEEP_LIMIT` jobs per cycle
   until all are updated. Remove the flag once done.

**Warning:** this increases LLM calls per cycle. The per-cycle circuit breaker
(`HUNT_SCORE_MAX_LLM_PER_CYCLE`) still applies; a large backlog will drain over
multiple cycles, not in one shot.

---

## Metrics and what they detect

### `gojob_hunt_fit_score` (histogram)

Fit-score distribution across LLM-scored jobs (0–100, buckets at 0/20/40/60/80/100).

**What to look for:**
- Skew toward low buckets (0–40) → most jobs are poor matches. Consider raising
  `HUNT_SCORE_MIN_JACCARD` to save LLM budget, or verify the scoring profile
  is up-to-date.
- All scores in the 80–100 bucket → Jaccard threshold too low (trivial jobs
  passing). Raise `HUNT_SCORE_MIN_JACCARD`.
- Distribution matches expectation → healthy.

**Tune:** use this histogram to calibrate `HUNT_NOTIFY_MIN_FIT`. If 90% of scores
are below 40 and you want only the top quartile notified, set `HUNT_NOTIFY_MIN_FIT=60`.

---

### `gojob_hunt_score_filtered_total{stage}` (counter)

Pre-LLM drop rate by stage. Labels: `recency` and `jaccard`.

| Label | Meaning | Playbook |
|---|---|---|
| `recency` | Job was stale (`posted_at` nil or older than `HUNT_NOTIFY_MAX_AGE`) | Normal churn. A sudden spike = ATS stopped sending `posted_at` dates. Cross-check `hunt_posted_at_total{present="false"}`. |
| `jaccard` | Job was below `HUNT_SCORE_MIN_JACCARD` keyword-overlap threshold | Normal filtering. If `jaccard` ≫ `recency`, the Jaccard threshold may be too aggressive. Lower `HUNT_SCORE_MIN_JACCARD`. |

A rising `jaccard` counter with flat LLM calls means the scorer is working
efficiently (cheap rejections before the expensive LLM). A falling `jaccard`
with rising LLM costs means the threshold is too low.

---

### `gojob_hunt_score_llm_total{result}` (counter)

LLM scorer outcome breakdown. Labels: `ok`, `enum_clamp`, `parse_fail`, `llm_error`.

| Label | Meaning | Playbook |
|---|---|---|
| `ok` | LLM returned valid JSON with spec-compliant enum values | Healthy |
| `enum_clamp` | JSON valid but `success_band` or `over_under` had an unknown value, clamped to default | LLM occasionally strays from the enum vocabulary. Acceptable at low rate; a sustained spike means the prompt instructions are being ignored. |
| `parse_fail` | LLM response could not be parsed as JSON (fail-open → `unscored`) | Spike = LLM proxy returning errors in plain text, or model changed. Check `hunt_notify_total{outcome="unscored"}` and the LLM proxy logs. |
| `llm_error` | LLM call itself returned an error (fail-open → `unscored`) | LLM proxy down, overloaded, or misconfigured. Check `llm_errors_total` and cliproxyapi health. |

**Failure-mode playbook:**

`parse_fail` or `llm_error` spiking:

```bash
# Check overall LLM error rate
curl -s http://localhost:9891/metrics | grep 'llm_errors_total'

# Check unscored notify rate (fail-open jobs)
curl -s http://localhost:9891/metrics | grep 'hunt_notify_total{outcome="unscored"}'

# Verify LLM proxy is healthy (cliproxyapi port 8317)
curl -s http://localhost:8317/health
```

If `HUNT_SCORE_FAIL_OPEN=true` (default), scoring failures do not block
notification — jobs are still notified with a degraded (recency-only) card.
Set `HUNT_SCORE_FAIL_OPEN=false` to drop unscored jobs instead.

---

## Failure-mode summary

| Symptom | Counter signal | First action |
|---|---|---|
| Jobs not appearing in `hunt_list` | `hunt_ingest_total` flat | Check `HUNT_INGEST_ENABLED`, platform errors |
| All scored jobs have low fit | `hunt_fit_score` histogram skewed low | Lower `HUNT_NOTIFY_MIN_FIT`, review scoring profile |
| Notifications dropping unexpectedly | `hunt_notify_total{outcome="low_fit"}` rising | Lower `HUNT_NOTIFY_MIN_FIT` or check profile |
| LLM-scored rate drops suddenly | `hunt_score_llm_total{result="llm_error"}` rising | Check cliproxyapi health |
| Garbled scores (`unscored` spike) | `hunt_score_llm_total{result="parse_fail"}` rising | LLM schema drift; check raw prompt/response in logs |
| Over-aggressive Jaccard filtering | `hunt_score_filtered_total{stage="jaccard"}` ≫ recency | Lower `HUNT_SCORE_MIN_JACCARD` |
| Budget exhausted before sweep | `HUNT_SCORE_MAX_LLM_PER_CYCLE` reached mid-cycle | Raise `HUNT_SCORE_MAX_LLM_PER_CYCLE` or split into more cycles |
| Backlog of unscored jobs | Sweep not keeping up | Raise `HUNT_SCORE_SWEEP_LIMIT` or run multiple cycles |
