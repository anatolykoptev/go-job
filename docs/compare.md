# go_job vs Competitors — Detailed Comparison

> Last updated: 2026-02-19

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
| **Google Jobs** | ❌ | ✅ | ❌ | ❌ | ✅ |
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
| Pagination (offset) | ❌ | ❌ | ✅ | ✅ |
| Results count limit | ❌ (fixed ~15) | ✅ | ✅ | ✅ |

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

### High Priority — Job Search Gaps

| Feature | Effort | Notes |
|---------|--------|-------|
| **Glassdoor** | Medium | Salary data + company reviews — critical for compensation research |
| **ZipRecruiter** | Medium | Large US market, many exclusive postings |
| **Google Jobs** | Low | Aggregator — broad coverage via SearXNG `site:jobs.google.com` |
| **Pagination / offset** | Low | LinkedIn Guest API supports `start=N`; add `offset` param |
| **`results_limit` param** | Low | Currently fixed at ~15 per source |

### Medium Priority — Resume & Career Tools (New MCP Tools)

These would make go_job a **complete career assistant**, not just a job finder:

| Tool | Description | Implementation |
|------|-------------|---------------|
| **`resume_analyze`** | Compare resume text vs job description → gap analysis, ATS score, missing keywords | LLM prompt + cosine similarity |
| **`cover_letter_generate`** | Generate tailored cover letter from resume + JD | LLM with structured prompt |
| **`resume_tailor`** | Rewrite resume sections to match specific JD keywords | LLM + diff output |
| **`salary_research`** | Aggregate salary data for role+location from multiple sources | Glassdoor SearXNG + levels.fyi scrape |
| **`company_research`** | Company overview: size, funding, reviews, tech stack, recent news | Crunchbase/LinkedIn/HN scrape |

### Low Priority — Automation

| Feature | Notes |
|---------|-------|
| **Job tracking** (save/status) | Store applied/interested jobs in local JSON/SQLite |
| **Duplicate detection** | Dedup across sources by title+company+location |
| **Alert/watch mode** | Periodic re-search + notify on new matches |

---

## Part 4: Ecosystem Map

```
Job Search ──────────────────────────────────────────────────────────────
  go_job (this)    ← MCP, 10+ sources, LLM summary, Go, no auth
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
