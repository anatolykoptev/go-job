# Changelog

All notable changes to go_job are documented here.

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
