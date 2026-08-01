# go_job vs Competitors — Detailed Comparison

> Last updated: 2026-07-31

## How to read this document

Everything below is a **feature** comparison, and features are the shallow layer. Sources,
filters, caching and TLS fingerprinting are all copyable in a weekend by anyone who decides
to; anti-bot bypass in particular is an arms race won by infrastructure scale, and several
of these boards will open an API eventually.

The rows that would actually be hard for a competitor to match are not in the tables,
because no competitor in this space has them either:

| Durable difference | Where it lives |
|---|---|
| Longitudinal record — what you saw, rejected, applied to, and how it went | `hunt_jobs`, `hunt_ratings`, `application_persist`, `resume_memory` |
| Provenance — every claim about you traceable to a recorded fact | `master_resume` (`is_master` / `parent_id` / atomic promotion) |
| Determinism you can audit — a ranked list that shows its arithmetic | `job_quality_score`, `job_match_score`; ranking pass 1 (planned) |
| Continuous operation — push, not pull | hunt ingest + Telegram notify |

Read the feature tables as "table stakes we have covered", not as the moat. The moat is
stated in [`architecture/principles.md`](architecture/principles.md).

---

## Part 1: Job Search MCP Servers

### Overview

| | **go_job** | jobspy-mcp-server | linkedin-mcp-server | mcp-linkedin | AIHawk |
|---|---|---|---|---|---|
| **Language** | Go | JavaScript | Python | Python | Python |
| **Stars** | — | 26 | 899 | 190 | 29 352 ⭐ |
| **Transport** | HTTP + stdio | stdio / SSE | stdio | stdio | CLI / n8n |
| **Type** | MCP server | MCP server | MCP server | MCP server | Automation bot |
| **LLM summarization** | ✅ | ❌ | ❌ | ❌ | ✅ (cover letters) |
| **Caching (L1+L2)** | ✅ Redis + memory | ❌ | ❌ | ❌ | ❌ |
| **No auth required** | ✅ | ✅ | ❌ (Playwright) | ❌ (email+pass) | ❌ (browser) |
| **Self-hosted** | ✅ | ✅ | ✅ | ✅ | ✅ |
| **Auto-apply** | ❌ | ❌ | ❌ | ❌ | ✅ LinkedIn EasyApply |

### Sources Coverage

| Source | **go_job** | jobspy-mcp | linkedin-mcp-server | AIHawk | JobSpy (lib) |
|--------|-----------|-----------|-------------------|--------|------------|
| **LinkedIn** | ✅ Guest API | ✅ scrape | ✅ Playwright | ✅ Selenium | ✅ |
| **Indeed** | ✅ SearXNG+scrape | ✅ scrape | ❌ | ✅ Selenium | ✅ |
| **Greenhouse** | ✅ public API | ❌ | ❌ | ❌ | ❌ |
| **Lever** | ✅ public API | ❌ | ❌ | ❌ | ❌ |
| **YC workatastartup** | ✅ | ❌ | ❌ | ❌ | ❌ |
| **HN Who is Hiring** | ✅ Algolia | ❌ | ❌ | ❌ | ❌ |
| **RemoteOK** | ✅ API | ❌ | ❌ | ❌ | ❌ |
| **WeWorkRemotely** | ✅ RSS | ❌ | ❌ | ❌ | ❌ |
| **Freelancer.com** | ✅ REST API | ❌ | ❌ | ❌ | ❌ |
| **Upwork** | ✅ SearXNG | ❌ | ❌ | ❌ | ❌ |
| **Хабр Карьера** | ✅ JSON API | ❌ | ❌ | ❌ | ❌ |
| **Glassdoor** | ❌ | ✅ | ❌ | ❌ | ✅ |
| **ZipRecruiter** | ❌ | ✅ | ❌ | ❌ | ✅ |
| **Google Jobs** | ✅ SearXNG | ✅ | ❌ | ❌ | ✅ |
| **Bayt / Naukri** | ❌ | ✅ | ❌ | ❌ | ✅ |

> **JobSpy** (`speedyapply/JobSpy`, 2786★) — Python library (not MCP), wraps LinkedIn/Indeed/Glassdoor/ZipRecruiter/Google. Used by jobspy-mcp-server under the hood.

### Filtering Parameters

| Filter | **go_job** | jobspy-mcp | linkedin-mcp-server | mcp-linkedin |
|--------|-----------|-----------|-------------------|-------------|
| Keywords / query | ✅ | ✅ | ✅ | ✅ |
| Location | ✅ | ✅ | ✅ | ✅ |
| Experience level | ✅ (6 levels) | ❌ | ✅ | ❌ |
| Job type (full/part/contract) | ✅ | ❌ | ✅ | ❌ |
| Remote / onsite / hybrid | ✅ | ✅ | ✅ | ❌ |
| Time range (day/week/month) | ✅ | ✅ (`hours_old`) | ✅ | ❌ |
| Salary filter | ✅ LinkedIn `f_SB2` | ❌ | ✅ (`40k+`…`200k+`) | ❌ |
| Platform / source filter | ✅ | ✅ (`site_names`) | ❌ | ❌ |
| Pagination (offset) | ✅ | ❌ | ✅ | ✅ |
| Results count limit | ✅ (default 15, max 50) | ✅ | ✅ | ✅ |

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
| LLM-generated summary | ✅ | ❌ | ❌ | ❌ |

---

## Part 2: Resume & Career Tools

### Resume Builders / CV Generators

| Project | Stars | Type | Key Features |
|---------|-------|------|-------------|
| **[olyaiy/resume-lm](https://github.com/olyaiy/resume-lm)** | 209★ | Web app (Next.js) | AI resume builder, cover letter generator, ATS scoring, PDF export, job-tailored versions |
| **[eyaab/cv-resume-builder-mcp](https://github.com/eyaab/cv-resume-builder-mcp)** | 11★ | MCP server (Python) | Auto-syncs from Git commits, Jira tickets, Credly certs; generates ATS-compliant LaTeX PDF |
| **[jsonresume/mcp](https://github.com/jsonresume/mcp)** | 59★ | MCP server (TypeScript) | Updates JSON Resume from codebase analysis; GitHub Gist storage; OpenAI-powered descriptions |
| **[Vinayaks439/LangFlow-MCP-High-ATS-Resume-creator](https://github.com/Vinayaks439/LangFlow-MCP-High-ATS-Resume-creator)** | 11★ | MCP server (LangFlow) | Multi-agent ATS-optimized resume generation via LangFlow low-code |
| **[marswangyang/Roger](https://github.com/marswangyang/Roger)** | 1★ | MCP server (Python) | Generates tailored LaTeX resumes + cover letters per job description |

### Resume Analysis / ATS Optimization

| Project | Stars | Type | Key Features |
|---------|-------|------|-------------|
| **[leelakrishnasarepalli/gapinmyresume-mcp](https://github.com/leelakrishnasarepalli/gapinmyresume-mcp)** | 0★ | MCP server (Python) | Resume vs JD gap analysis, missing keywords, ATS compatibility, GPT-4o-mini |
| **[saiprasaad2002/FastAPI-MCP-Server](https://github.com/saiprasaad2002/Job-Application-Agent-MCP)** | 2★ | MCP server (Python) | PDF/DOCX parsing, cosine similarity resume↔JD, LLM validation |
| **[sms03/resume-mcp](https://github.com/sms03/resume-mcp)** | 0★ | MCP server (Python) | Resume sorting by JD relevance, Google ADK |

### Full Automation (Apply Bots)

| Project | Stars | Type | Key Features |
|---------|-------|------|-------------|
| **[feder-cr/Jobs_Applier_AI_Agent_AIHawk](https://github.com/feder-cr/Jobs_Applier_AI_Agent_AIHawk)** | 29 352★ | Python bot | LinkedIn + Indeed scraping, AI cover letters, auto-apply EasyApply, resume upload, ATS form filling |
| **[GodsScion/Auto_job_applier_linkedIn](https://github.com/GodsScion/Auto_job_applier_linkedIn)** | 1 630★ | Python bot | LinkedIn EasyApply automation, Selenium, undetected-chromedriver |
| **[imon333/Job-apply-AI-agent](https://github.com/imon333/Job-apply-AI-agent)** | 107★ | Python + n8n | LinkedIn/Indeed/StepStone scraping, custom CV+cover letter per job, Google Sheets tracking |
| **[AloysJehwin/job-app](https://github.com/AloysJehwin/job-app)** | 53★ | n8n workflow | Resume extraction, job matching, resume rewriting to fit JD, Google Drive/Sheets storage |

### Interview Preparation

| Project | Stars | Type | Key Features |
|---------|-------|------|-------------|
| **[proyecto26/TheJobInterviewGuide](https://github.com/proyecto26/TheJobInterviewGuide)** | 422★ | Guide | Behavioral, coding, system design interview prep; updated 2026 |

---

## Part 3: What go_job Should Add

### High priority — the ranking layer

Not a source. A source is a commodity; the decision made over the sources is not.

Today the relevance decision is taken inside an LLM prompt, which means it cannot be
tested, mutated or regression-guarded — and on 2026-07-31 it returned zero jobs on five
consecutive live queries without anything going red. The work is specified in
[roadmap.md → Phase 12](roadmap.md) and
[principles.md §5](architecture/principles.md#5-the-search-architecture):
a mechanical pass 1 (BM25 + embeddings fused with RRF) that always shows its arithmetic,
then a model pass over the top-K fed **the user's own history** rather than the query text.

That second pass is the only part of this comparison a chat window cannot reproduce.

### Medium priority — additional sources

| Source | Effort | Notes |
|--------|--------|-------|
| **Glassdoor** | Medium | Salary data + company reviews |
| **ZipRecruiter** | Medium | Large US market, many exclusive postings |

### Already shipped — previously listed here as gaps

| Feature | Where |
|---------|-------|
| Job tracking (save/status) | `job_tracker`, Postgres `hunt_jobs` + `hunt_ratings` (ADR-002) |
| Duplicate detection | `DedupByDomain` + hunt-store upsert on URL |
| Alert/watch mode | hunt ingest cycle + Telegram notify (`internal/hunt/notify`) |

---

## Part 4: Ecosystem Map

```
Job Search ──────────────────────────────────────────────────────────────
  go_job (this)    ← MCP, 17 connectors, 32 tools, Go, no auth, persistent hunt store
  jobspy-mcp       ← MCP, wraps JobSpy, 7 sources incl. Glassdoor
  linkedin-mcp-server ← MCP, Playwright, 899★, LinkedIn only
  AIHawk           ← Bot, 29k★, auto-apply, LinkedIn+Indeed

Resume Tools ────────────────────────────────────────────────────────────
  resume-lm        ← Web app, ATS score, cover letter, 209★
  cv-resume-builder-mcp ← MCP, Git+Jira+Credly → LaTeX PDF
  jsonresume/mcp   ← MCP, codebase → JSON Resume → GitHub Gist
  Roger (MCP)      ← MCP, tailored LaTeX resume + cover letter per JD

ATS Analysis ────────────────────────────────────────────────────────────
  gapinmyresume-mcp ← MCP, resume vs JD gap, GPT-4o-mini
  FastAPI-MCP-Server ← MCP, PDF parse + cosine similarity

Auto-Apply Bots ─────────────────────────────────────────────────────────
  AIHawk           ← 29k★, LinkedIn EasyApply + AI cover letters
  Auto_job_applier ← 1.6k★, LinkedIn Selenium
  imon333/job-app  ← n8n, multi-site, CV per job, Sheets tracking
```

---

## Competitor Links

**Job Search MCP:**
- [borgius/jobspy-mcp-server](https://github.com/borgius/jobspy-mcp-server) — JS, wraps Python JobSpy, 26★
- [stickerdaniel/linkedin-mcp-server](https://github.com/stickerdaniel/linkedin-mcp-server) — Python, Playwright, 899★
- [adhikasp/mcp-linkedin](https://github.com/adhikasp/mcp-linkedin) — Python, unofficial API, 190★
- [speedyapply/JobSpy](https://github.com/speedyapply/JobSpy) — Python library (not MCP), 2786★

**Resume & Career:**
- [olyaiy/resume-lm](https://github.com/olyaiy/resume-lm) — Web app, ATS + cover letter, 209★
- [eyaab/cv-resume-builder-mcp](https://github.com/eyaab/cv-resume-builder-mcp) — MCP, Git+Jira→LaTeX, 11★
- [jsonresume/mcp](https://github.com/jsonresume/mcp) — MCP, codebase→resume, 59★
- [feder-cr/Jobs_Applier_AI_Agent_AIHawk](https://github.com/feder-cr/Jobs_Applier_AI_Agent_AIHawk) — Auto-apply bot, 29 352★
