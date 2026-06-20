# Changelog

All notable changes to go_job are documented here.

## [1.4.0](https://github.com/anatolykoptev/go-job/compare/v1.3.2...v1.4.0) (2026-06-20)


### Features

* **fetcher:** bump go-engine v1.12.1 + opt into direct-first tiered fallback ([#9](https://github.com/anatolykoptev/go-job/issues/9)) ([75e1d52](https://github.com/anatolykoptev/go-job/commit/75e1d52502d48e2df0df46521dc7362a81833b9a))
* **fetcher:** wire go-engine v1.13.0 tier router metrics ([#11](https://github.com/anatolykoptev/go-job/issues/11)) ([f927051](https://github.com/anatolykoptev/go-job/commit/f9270519b549629c7907fcf9b5e3f6af610fd974))
* **go-job:** tracker→uploads + Ashby + ATS tools + ratelimit + breaker ([#22](https://github.com/anatolykoptev/go-job/issues/22)) ([7d26d0f](https://github.com/anatolykoptev/go-job/commit/7d26d0f17fb5bf3e6558f3d287159c53c12be143))
* **hunt:** Phase 1 — domain-typed persistent tables + L1 url-hash dedup ([#17](https://github.com/anatolykoptev/go-job/issues/17)) ([fc7fa8d](https://github.com/anatolykoptev/go-job/commit/fc7fa8dbe9f319e10228cec38d47b4a5ec263d71))
* **hunt:** Phase 2 — canonical URL normalizer for cross-source dedup ([#18](https://github.com/anatolykoptev/go-job/issues/18)) ([756d412](https://github.com/anatolykoptev/go-job/commit/756d412cbb1ac1b7d72bef2925ca417090e340a1))
* **hunt:** status enrichment via lazy on-read + drop hand-rolled monitors ([#19](https://github.com/anatolykoptev/go-job/issues/19)) ([54f24f1](https://github.com/anatolykoptev/go-job/commit/54f24f179eab14e5625e64f479a361d8e69a1b99))
* Inspira (careers.un.org) + UNDP scrapers for job_search ([#25](https://github.com/anatolykoptev/go-job/issues/25)) ([5c0a7d3](https://github.com/anatolykoptev/go-job/commit/5c0a7d33fd20a076ba31bc7cf4dc5155ca4d4732))
* **jobs:** Cantina + Code4rena audit contest sources ([#13](https://github.com/anatolykoptev/go-job/issues/13)) ([d3642ff](https://github.com/anatolykoptev/go-job/commit/d3642ff03a418eb6671087fcce85cf2917f2960b))
* **jobs:** Sherlock audit contest source (+ pre-commit lint cleanup) ([#12](https://github.com/anatolykoptev/go-job/issues/12)) ([ee0273d](https://github.com/anatolykoptev/go-job/commit/ee0273dad66aad3552af2fcf90f7000ca46c288c))
* **jobs:** wire Sherlock/Cantina/Code4rena into security_monitor + Prometheus counters ([#14](https://github.com/anatolykoptev/go-job/issues/14)) ([0a679c8](https://github.com/anatolykoptev/go-job/commit/0a679c85853742c47ea56fc49f1e3f3df7fb52d9))
* **llm:** wire LLM_MODEL_FALLBACK chain (Phase 2) ([362be56](https://github.com/anatolykoptev/go-job/commit/362be56192d677ebe05ec604dcb5474809aff5c6))
* **llm:** wire LLM_MODEL_FALLBACK chain (Phase 2) ([4a42e84](https://github.com/anatolykoptev/go-job/commit/4a42e84966240a42edccbfdcef6ff283f12b3ee9))
* **oversize:** Postgres spillover for large MCP responses + go-kit v0.65.0 ([#15](https://github.com/anatolykoptev/go-job/issues/15)) ([59c9b1c](https://github.com/anatolykoptev/go-job/commit/59c9b1ca290c2fcc6acc8165577f993b17ffe472))
* send NERV_MCP_TOKEN bearer on go-nerv client ([#24](https://github.com/anatolykoptev/go-job/issues/24)) ([3cb0aa5](https://github.com/anatolykoptev/go-job/commit/3cb0aa51040aa292dd90af960c13db30af63ae51))


### Bug Fixes

* **hunt:** tag source=inspira/undp for UN scrapers ([#26](https://github.com/anatolykoptev/go-job/issues/26)) ([8b97c3c](https://github.com/anatolykoptev/go-job/commit/8b97c3cbe10b4a0f203c0584fa23bd923781faf6))
* **master_resume:** abort on clear errors, flag truncated resumes ([#6](https://github.com/anatolykoptev/go-job/issues/6)) ([6c30cf8](https://github.com/anatolykoptev/go-job/commit/6c30cf823125465246c39f3f4abc4530b319d67e))
* **memdb:** retry DeleteByUser on transient deadlock 500s ([#7](https://github.com/anatolykoptev/go-job/issues/7)) ([fc39f26](https://github.com/anatolykoptev/go-job/commit/fc39f26f6104d4ceff1ed5dabba7c51c3629177a))
* **memdb:** use bulk delete_all_memories endpoint for clear ([#8](https://github.com/anatolykoptev/go-job/issues/8)) ([f5bd60d](https://github.com/anatolykoptev/go-job/commit/f5bd60d71cebb7077b219bb47b2999f98d3e01f0))
* **test:** align TestBuildSourcesTextTruncation with go-engine v1.12 semantic ([#10](https://github.com/anatolykoptev/go-job/issues/10)) ([1b7aad2](https://github.com/anatolykoptev/go-job/commit/1b7aad28c0250576d113766cd085730ad2e24d57))

## [1.0.0] — 2026-02-20

First production release.

### New Tools (4 MCP tools total)

- **`job_search`** — LinkedIn, Greenhouse, Lever, YC, HN, Indeed, Хабр Карьера
- **`remote_work_search`** — RemoteOK, WeWorkRemotely, Remotive, SearXNG
- **`freelance_search`** — Freelancer.com (direct API), Upwork (SearXNG)
- **`job_match_score`** — Jaccard keyword scoring: resume vs job listings (0–100)

Plus 7 career tools: `resume_analyze`, `resume_tailor`, `cover_letter_generate`, `company_research`, `salary_research`, `job_tracker_add/list/update`.

### Highlights

#### job_search
- **Indeed GraphQL API** — internal iOS app endpoint, structured salary ranges, SearXNG fallback
- **LinkedIn pagination** — up to 50 results per query (was 25 max)
- **LinkedIn Easy Apply filter** — `easy_apply: true` → `f_JIYN=true`
- **LinkedIn geo_id** — 42 cities/countries map to precise LinkedIn geoId (more accurate than text location)
- **Structured salary** — `salary_min`, `salary_max`, `salary_currency`, `salary_interval` alongside human-readable `salary`
- **Canonical deduplication** — cross-source dedup by normalized job title (strips "at CompanyName", collapses punctuation)
- **Indeed + Habr wired** — were defined but not called; now proper parallel sources

#### remote_work_search
- **Remotive** — free public JSON API (`remotive.com/api/remote-jobs?search=...`), no auth required
- Now 3 direct API sources + SearXNG

#### job_match_score (new)
- Extracts keywords from resume once, scores all jobs in batch
- Jaccard similarity: `|resume ∩ job| / |resume ∪ job| × 100`
- Returns `matching_keywords` (your strengths for this role) and `missing_keywords` (skills gap)
- Tech-aware tokenizer: preserves `c++`, `c#`, `node.js`

### Architecture
- Fully standalone module (`github.com/anatolykoptev/go_job`) — no dependency on go-search
- Chrome TLS fingerprint (`bogdanfinn/tls-client`) for anti-bot bypass on LinkedIn/Indeed
- 2-tier cache: L1 in-memory + L2 Redis (graceful fallback to L1 if Redis unavailable)
- Exponential backoff retry on all HTTP calls

## [0.9.0] — 2026-02-15

- AIHawk-level career assistant: 8 new MCP tools (resume_analyze, cover_letter_generate, resume_tailor, salary_research, company_research, job_tracker_*)
- Full test suite for new tools
- Per-tool documentation in `docs/tools/`

## [0.8.0] — 2026-02-10

- Decoupled from go-search into standalone module
- Greenhouse + Lever ATS sources
- HN Who is Hiring integration (Algolia)
- YC workatastartup.com scraper
- Habr Карьера API client
- Indeed SearXNG fallback
