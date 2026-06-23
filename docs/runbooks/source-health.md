# Source Health Runbook

## Overview

go-job fans out `job_search` calls across up to 17 parallel connectors. Each
connector's result is classified into a bounded outcome label and emitted to
`gojob_platform_results_total{platform,outcome}`. This metric is the single
entry point for answering "which source is broken right now?" without reading
container logs.

**Endpoint:** `curl http://localhost:9891/metrics | grep platform_results`

---

## Outcome vocabulary (ADR-J3, P3)

| Outcome | Meaning | Likely cause |
|---------|---------|--------------|
| `ok` | Source returned ≥1 result | Healthy |
| `empty` | Source returned 0 results, no error | Query had no matches on that platform, or rate-limited silently |
| `error` | Generic error — network, HTTP 4xx/5xx, etc. | Connectivity, upstream API change, TLS, rate limit |
| `timeout` | `context.DeadlineExceeded` or `context.Canceled` | Source too slow; per-source budget exceeded |
| `no_key` | Required API key absent in config | `INDEED_API_KEY` (or equivalent) not set in deploy env |
| `parse_fail` | Response body fails to unmarshal | API schema drift — connector struct no longer matches live API |

---

## Step 1 — Read the metrics

```bash
curl -s http://localhost:9891/metrics | grep platform_results
```

Or via compose network (from the host):

```bash
docker exec go-job wget -qO- http://localhost:9891/metrics | grep platform_results
```

Example healthy output:

```
gojob_platform_results_total{platform="greenhouse",outcome="ok"} 42
gojob_platform_results_total{platform="habr",outcome="empty"} 3
gojob_platform_results_total{platform="indeed",outcome="no_key"} 7
```

---

## Step 2 — Triage by outcome

### `parse_fail` — schema drift

The connector's Go struct no longer matches the API's JSON shape.

1. Identify the platform: `grep 'parse_fail' <metrics output>`
2. Diff the connector's struct against the live API response:
   - **habr** (`internal/engine/jobs/habr.go`): the `Employment` field changed from
     `struct{Title string}` to a plain string. Fix in P4(b).
3. Open a P4 PR fixing the struct. Verify the outcome flips to `ok`.

### `no_key` — missing API key

A keyed source has no key configured. The source is cleanly skipped (not erroring).

1. Identify the platform: `grep 'no_key' <metrics output>`
2. Check the relevant env key:
   - **indeed**: `INDEED_API_KEY` in `~/deploy/krolik-server/.env`
3. Add the key and restart the container. Verify outcome flips to `ok`.
4. If no key is available, the source will remain `no_key` — this is expected
   and the counter is informational. P4(c) adds `NeedsAPIKey` capability so the
   source is skipped silently in the fan-out (no counter increment).

### `timeout` — source too slow

The connector hit its per-call context deadline.

1. Identify the platform: `grep 'timeout' <metrics output>`
2. Common offenders: `hn` (Algolia+Firebase fan-out), `inspira`, `undp`.
3. Check the source's timeout budget. `hn` has `hnFanoutBudget = 30s`
   (`internal/engine/jobs/hnjobs.go`).
4. Consider marking `hn`/`inspira`/`undp` as `OptIn` capability (excluded from
   `platform=all`) if interactive latency matters more than coverage. P4(e/f).
5. Check upstream API health.

### `error` — generic failure

Connectivity, HTTP error, or unexpected error not covered by the above classes.

1. Identify the platform: `grep '"error"' <metrics output>`
2. Check container logs for the specific error:
   ```bash
   docker logs go-job --since 1h | grep '<platform>'
   ```
3. Common causes: Webshare proxy pool exhausted, upstream rate limit, TLS
   handshake failure.

### `empty` — no matches

The source is healthy but returned zero results. This is normal for niche queries.

Alert condition: `empty/(ok+empty)` trending to 1.0 for a platform that
previously returned results. Signals a broken discovery dependency, not just a
dry query.

---

## Step 3 — Source isolation

Each source is isolated. A `parse_fail` on habr does not affect greenhouse, linkedin,
or any other platform. The fan-out always completes; a failing source contributes
0 results and its outcome counter, nothing else.

**No emergency action required** for a single-source failure in best-effort dev
tooling. Open a P4 PR targeting the specific source.

---

## Alert routing (P3 goal)

Alert condition (once Prometheus scrape is configured):

```promql
# A source that was working recently started erroring.
increase(gojob_platform_results_total{outcome=~"error|parse_fail|no_key"}[1h]) > 5
```

Severity: `warning` — routes via dozor webhook, 5-minute hold, 12-hour grouping.

---

## Source duration

Per-source Fetch latency histogram: `gojob_source_duration_seconds{platform}`.

```bash
curl -s http://localhost:9891/metrics | grep source_duration
```

P99 latency over 30s indicates a slow-fan-out source (hn, inspira, undp) — these
are candidates for the `OptIn` capability flag (P4).

---

## Related metrics

| Metric | Meaning |
|--------|---------|
| `gojob_platform_results_total{platform,outcome}` | Primary source-health signal |
| `gojob_source_duration_seconds{platform}` | Per-source Fetch latency histogram |
| `gojob_hunt_discovery_urls_total{platform}` | ATS slug-discovery yield (greenhouse/lever/ashby) |
| `gojob_hunt_discovery_source_total{source}` | go-search vs local-fallback discriminator |
