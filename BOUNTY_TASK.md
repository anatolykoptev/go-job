# Bounty Objective: Most HTML-scraping connectors bypass stealth entirely — standardise on ox-browser POST /fetch
Target: anatolykoptev/go-job Issue #348
URL: https://github.com/anatolykoptev/go-job/issues/348

## Issue Description
Most job connectors scrape HTML from consumer sites with a bare `net/http` client — Go's TLS fingerprint, a Go User-Agent, and a datacenter IP. Only five of them use any anti-bot transport at all.

Measured on `main`, counting transport references per file in `internal/engine/jobs/`:

| connector | transport |
|---|---|
| `craigslist.go` | `engine.Cfg.BrowserClient` (stealth) |
| `indeed.go` | `engine.Cfg.BrowserClient` (stealth) |
| `ycjobs.go` | `engine.Cfg.BrowserClient` (stealth) |
| `linkedin.go` | `engine.FetchProxyBody` (stealth → proxy → ox-browser) |
| `himalayas.go` | `engine.FetchProxyBody` |
| everything else (~25 files) | bare `net/http` |

Bare is not wrong everywhere. Splitting the rest by what they actually fetch:

**Legitimately bare — documented JSON APIs, often token-authenticated. Leave alone.**
`api.github.com` (`sherlock.go`, `github_prs.go`, `algora_github.go`), `api.ashbyhq.com` / `api.lever.co` / `boards.greenhouse.io` (`ats.go`), `api.boss.dev`, `api.bountyhub.dev`, `raw.githubusercontent.com` (`security_bounty.go`). Routing these through a browser tier would add latency and a failure mode for nothing.

**Candidates — HTML scraped from a consumer site, no anti-bot transport:**
`habr.go` (career.habr.com) · `remotejobs.go` (remoteok.com, remotive.com, weworkremotely.com) · `inspira.go` (careers.un.org) · `undp.go` (Oracle HCM, `estm.fa.em2.oraclecloud.com`) · `immunefi.com` · `algora_jobs.go` + `algora_api.go` (algora.io, github.com HTML) · `collaborators.build` · `app.lightningbounties.com` · `app.opire.dev` · `hnjobs.go` (news.ycombinator.com — tolerant, lowest priority)

Each of these fails the same way Craigslist did in #346: the site refuses or challenges, the connector returns zero results, and the caller cannot distinguish that from "no jobs matched". #340 makes the outcome visible per source, but visible-and-blocked is still blocked.

### The transport to standardise on

ox-browser exposes `POST /fetch` (`crates/js/src/fetch.rs`) — "fast wreq+BoringSSL fetch without headless browser". Chrome TLS/JA3 impersonation with a proxy pool and CF solver behind it, returning the raw body with no extraction. Measured against Craigslist: 200, 194 KB, 344 listings, 0.4s, versus 403 for a plain GET of the same site's feed. It reports the upstream `status`, plus `cf_detected` / `cf_type`, so a block is distinguishable from a transport error — which is what makes an honest per-source outcome possible at all.

Note this is **not** what go-job currently calls. `internal/engine/config.go:60-62` wires the ox-browser tier to `/fetch-smart`, whose own source marks it `DEPRECATED: Use POST /read instead` — and `/read` runs Readability, which on a search-results page returns the site's hidden loading overlay rather than the listings (ox-browser#110). So the documented successor is wrong for this use case and `/fetch` is the right target.

### Proposed shape

A single helper in `internal/engine/jobs` that every HTML-scraping connector calls: stealth Chrome-TLS first, ox-browser `POST /fetch` on refusal, with the block/transport/empty distinction preserved in the returned error. Connectors on documented APIs keep their bare client. One connector migrated per commit so a regression is attributable.

Blocked on #346 landing first — it is the reference implementation of the ladder and the error classification.


## Instructions for Kuro (Autonomous Solver):
1. Use skills: `systematic-debugging` and `code-review-and-quality`.
2. Inspect the repository structure, locate relevant files, and understand the bug/feature.
3. Apply the minimal, robust fix or implementation.
4. Run any existing test suites or linters in the workspace.
5. Ensure the fix adheres to the project's existing code style.
6. When done, output a summary of files modified and verification results.
