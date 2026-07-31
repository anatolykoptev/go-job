```{=typst}
#text(size: 26pt, weight: "bold", fill: rgb("#0f172a"), tracking: -0.4pt)[Jordan Avery]
#v(1.6mm)
#line(length: 100%, stroke: rgb("#cbd5e1") + 0.7pt)
#v(2.4mm)
#text(size: 11pt, weight: "semibold", fill: rgb("#1e293b"))[Platform Engineer  ·  Storage, Scheduling, Go]
#linebreak()
#v(0.8mm)
#text(size: 10pt, fill: rgb("#64748b"))[Portland, OR  ·  jordan\@example.invalid  ·  github.com/example  ·  linkedin.com/in/example]
```

## Summary

Platform engineer who builds storage systems and runs them. Author of two permissively licensed Go libraries, including a block-cache library used by three services in production. Ten years building and operating systems that stay up.

## Selected Open Source

### example-cache · MIT · v3.4.0 · 18.2K LOC · 210 commits

#### A tiered block cache for Go services · github.com/example/example-cache

- Author of a two-tier cache: memory over disk, with a single eviction clock shared by both tiers.
- Designed the disk tier to survive an unclean shutdown by writing a checksum ahead of every block.
- Defined 9 append-only error codes as wrapped sentinels so callers classify with `errors.Is`, never string matching.
- Separated `entry_evicted` from `entry_absent` because a caller retrying an evicted key needs a different backoff than one asking for a key that never existed.
- Added a lock-ordering test that guards the two tiers against deadlock under concurrent promotion.

### example-sched · Apache-2.0 · v0.9.1 · 24K LOC · 6 packages

#### A fair-share job scheduler with backpressure · github.com/example/example-sched

- Built a scheduler that admits work only when the downstream queue proves it has room, rather than admitting and shedding later.
- Gated the fast admission path on a corroborating depth sample, trading throughput for predictability because a wrong admission costs a retry storm.
- Instrumented every rejection with a reason label so a silent drop became a countable outcome.

### example-kit · Go platform library

#### Shared primitives across the fleet · github.com/example/example-kit

- Designed a 12-package library now carrying six production services.
- Shipped a circuit breaker, a hedged-request helper, and an advisory-lock migration runner.

## Selected Technical Expertise

**Languages** Go primary, Rust secondary, Python working.

**Storage** Ran a tiered block cache in production, including crash-consistent recovery and checksum verification on every read.

**Distributed systems** Verified fan-out to **4,000 subscribers per topic**, and designed durable workflows on PostgreSQL.

**Operations** Operated Docker and systemd on aarch64 with self-hosted CI, OpenTelemetry, Prometheus and Loki.

## Selected Results

| Subsystem | Before | After |
| --- | --- | --- |
| Cache promotion | 310 ms | 0.4 ms |
| Corpus ingest | 4 h | 35 min |
| Admission decisions | 900 rps | 4,100 rps |

## Experience

### Staff Engineer, Example Systems · 2021–Present · Portland, OR

#### Storage and scheduling for a six-service platform

Operate a six-service platform across four servers, on call in a two-person rotation.

- Cut a cache promotion path from **310 ms to 0.4 ms**, via a composite index over the block manifest.
- Reduced corpus ingest from **4 hours to 35 minutes** on the same hardware.
- Diagnosed a **64 MiB cgroup** killing a worker mid-request across 40 restarts while the API reported an empty result set.
- Rebuilt that path so blocked, genuinely empty, and parse-failure became distinguishable outcomes rather than one empty list.

### Senior Engineer, Example Data · 2016–2021 · Remote

#### Query serving and schema evolution

- Migrated production from one row store to another under live traffic, writing the migration tooling.
- Owned the storage stack across two platform generations.

## Upstream Contributions

- Contributed a ~600-line storage backend implementing the full driver interface to [an upstream project](https://example.invalid/upstream), plus two further merged fixes.
- Exposed tokenizer options in an upstream full-text-search extension.

## Education

B.S., Computer Science. Example State University, 2012–2016.

Authorized to work in the US. Portland, OR, open to hybrid.
