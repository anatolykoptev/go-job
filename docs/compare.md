# go_job vs Competitors — Detailed Comparison

> Last updated: 2026-02-19

## Overview

| | **go_job** | jobspy-mcp-server | linkedin-mcp-server | mcp-linkedin | Hritik003/linkedin-mcp |
|---|---|---|---|---|---|
| **Language** | Go | JavaScript | Python | Python | Python |
| **Stars** | — | 26 | 899 | 190 | 29 |
| **Transport** | HTTP + stdio | stdio / SSE | stdio | stdio | stdio |
| **LLM summarization** | ✅ | ❌ | ❌ | ❌ | ❌ |
| **Caching (L1+L2)** | ✅ Redis + memory | ❌ | ❌ | ❌ | ❌ |
| **No auth required** | ✅ | ✅ | ❌ (Playwright) | ❌ (email+pass) | ❌ (Playwright) |
| **Self-hosted** | ✅ | ✅ | ✅ | ✅ | ✅ |

---

## Sources Coverage

| Source | **go_job** | jobspy-mcp | linkedin-mcp-server | mcp-linkedin |
|--------|-----------|-----------|-------------------|-------------|
| **LinkedIn** | ✅ Guest API | ✅ scrape | ✅ Playwright | ✅ unofficial API |
| **Indeed** | ✅ SearXNG+scrape | ✅ scrape | ❌ | ❌ |
| **Greenhouse** | ✅ public API | ❌ | ❌ | ❌ |
| **Lever** | ✅ public API | ❌ | ❌ | ❌ |
| **YC workatastartup** | ✅ | ❌ | ❌ | ❌ |
| **HN Who is Hiring** | ✅ Algolia | ❌ | ❌ | ❌ |
| **RemoteOK** | ✅ API | ❌ | ❌ | ❌ |
| **WeWorkRemotely** | ✅ RSS | ❌ | ❌ | ❌ |
| **Freelancer.com** | ✅ REST API | ❌ | ❌ | ❌ |
| **Upwork** | ✅ SearXNG | ❌ | ❌ | ❌ |
| **Glassdoor** | ❌ | ✅ | ❌ | ❌ |
| **ZipRecruiter** | ❌ | ✅ | ❌ | ❌ |
| **Google Jobs** | ❌ | ✅ | ❌ | ❌ |
| **Bayt / Naukri** | ❌ | ✅ | ❌ | ❌ |
| **Хабр Карьера** | ❌ | ❌ | ❌ | ❌ |

---

## Feature Comparison

### Filtering Parameters

| Filter | **go_job** | jobspy-mcp | linkedin-mcp-server | mcp-linkedin |
|--------|-----------|-----------|-------------------|-------------|
| Keywords / query | ✅ | ✅ | ✅ | ✅ |
| Location | ✅ | ✅ | ✅ | ✅ |
| Experience level | ✅ (6 levels) | ❌ | ✅ | ❌ |
| Job type (full/part/contract) | ✅ | ❌ | ✅ | ❌ |
| Remote / onsite / hybrid | ✅ | ✅ | ✅ | ❌ |
| Time range (day/week/month) | ✅ | ✅ (`hours_old`) | ✅ | ❌ |
| Salary filter | ❌ | ❌ | ✅ (`40k+`…`200k+`) | ❌ |
| Platform / source filter | ✅ | ✅ (`site_names`) | ❌ | ❌ |
| Pagination (offset) | ❌ | ❌ | ✅ | ✅ |
| Results count limit | ❌ (fixed) | ✅ | ✅ | ✅ |

### Output Quality

| Feature | **go_job** | jobspy-mcp | linkedin-mcp-server | mcp-linkedin |
|---------|-----------|-----------|-------------------|-------------|
| Structured JSON output | ✅ | ✅ (JSON/CSV) | ✅ | ✅ |
| LLM-generated summary | ✅ | ❌ | ❌ | ❌ |
| Salary data | ✅ (when available) | ✅ | ✅ | ❌ |
| Skills list | ✅ | ✅ | ❌ | ❌ |
| Job description | ✅ (truncated) | ✅ | ✅ | ✅ |
| Company info | ✅ | ✅ | ✅ | ✅ |
| Posted date | ✅ | ✅ | ✅ | ❌ |
| Application URL | ✅ | ✅ | ✅ | ✅ |

### Technical

| Feature | **go_job** | jobspy-mcp | linkedin-mcp-server | mcp-linkedin |
|---------|-----------|-----------|-------------------|-------------|
| Parallel source fetching | ✅ goroutines | ❌ | ❌ | ❌ |
| Result caching | ✅ L1+L2 Redis | ❌ | ❌ | ❌ |
| HTTP retry + backoff | ✅ | ❌ | ❌ | ❌ |
| TLS fingerprint spoofing | ✅ (LinkedIn) | ❌ | via Playwright | ❌ |
| Headless (no browser) | ✅ | ✅ | ❌ | ❌ |
| Rate limit handling | ✅ staggered delays | ❌ | via Playwright | ❌ |
| Metrics endpoint | ✅ `/metrics` | ❌ | ❌ | ❌ |
| Health endpoint | ✅ `/health` | ❌ | ❌ | ❌ |

---

## go_job Unique Advantages

1. **Only Go implementation** — lower memory, faster startup, single binary deploy
2. **Most sources** — 10+ sources vs 1-7 for competitors
3. **LLM summarization** — AI-generated structured output, not raw scrape data
4. **Two-tier caching** — Redis L2 + in-memory L1, configurable TTL
5. **Freelance search** — unique: Upwork + Freelancer.com, no competitor covers this
6. **HN Who is Hiring** — unique: Algolia search within monthly thread
7. **Greenhouse + Lever APIs** — direct public board APIs, not scraping
8. **No credentials required** — all sources work without login/API keys

---

## go_job Known Gaps (Roadmap)

| Gap | Priority | Notes |
|-----|----------|-------|
| Salary filter for LinkedIn | High | Add `f_SB2` param: `40k+`…`200k+` |
| Glassdoor | Medium | Salary research + company reviews |
| Хабр Карьера | Medium | Russian-speaking market, unique niche |
| Pagination / offset | Medium | LinkedIn Guest API supports `start=N` |
| `results_limit` parameter | Low | Currently fixed at ~15 |
| Glassdoor salary data | Low | Useful for compensation research |
| ZipRecruiter | Low | US market |
| Google Jobs | Low | Aggregator, broad coverage |

---

## Competitor Links

- [borgius/jobspy-mcp-server](https://github.com/borgius/jobspy-mcp-server) — JS, wraps Python JobSpy
- [stickerdaniel/linkedin-mcp-server](https://github.com/stickerdaniel/linkedin-mcp-server) — Python, Playwright-based, 899★
- [adhikasp/mcp-linkedin](https://github.com/adhikasp/mcp-linkedin) — Python, unofficial LinkedIn API, 190★
- [Hritik003/linkedin-mcp](https://github.com/Hritik003/linkedin-mcp) — Python, auto-apply feature, 29★
