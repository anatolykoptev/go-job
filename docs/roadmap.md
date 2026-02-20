# go_job + vaelor-jobs — Career Assistant Roadmap

> AIHawk-level career assistant through vaelor-jobs agent + go_job MCP server.
> Last updated: 2026-02-19

---

## Vision

Full career pipeline through a single AI agent (vaelor-jobs):

```
Find Jobs → Research → Prepare Application → Track Pipeline → Negotiate Offer
```

No browser automation. No credentials. Pure API + LLM.

---

## Implemented ✅

### Phase 1 — Job Search (go_job v1.0)
| Tool | Sources | Status |
|------|---------|--------|
| `job_search` | LinkedIn Guest API, Greenhouse, Lever, YC, HN, Indeed, Хабр Карьера | ✅ |
| `remote_work_search` | RemoteOK API, WeWorkRemotely RSS | ✅ |
| `freelance_search` | Freelancer.com REST API, Upwork SearXNG | ✅ |

**Filters:** experience, job_type, remote, time_range, salary (LinkedIn f_SB2), platform, location

### Phase 2 — Resume & Cover Letter (go_job v1.1)
| Tool | Description | Status |
|------|-------------|--------|
| `resume_analyze` | ATS score (0-100), missing keywords, gaps, recommendations | ✅ |
| `cover_letter_generate` | Tailored cover letter (professional/friendly/concise) | ✅ |
| `resume_tailor` | Rewrite resume sections to match JD, keyword diff | ✅ |

### Phase 3 — Research (go_job v1.1)
| Tool | Description | Status |
|------|-------------|--------|
| `salary_research` | p25/median/p75 from levels.fyi, Glassdoor, hh.ru, Хабр | ✅ |
| `company_research` | Size, funding, tech stack, culture, Glassdoor rating, news | ✅ |

### Phase 4 — Job Tracker (go_job v1.1)
| Tool | Description | Status |
|------|-------------|--------|
| `job_tracker_add` | Save job to local SQLite (~/.go_job/tracker.db) | ✅ |
| `job_tracker_list` | List by status: saved/applied/interview/offer/rejected | ✅ |
| `job_tracker_update` | Update status and notes by ID | ✅ |

### Phase 5 — vaelor-jobs Agent Skills
| Skill | Description | Status |
|-------|-------------|--------|
| `job-search` | Job/remote/freelance search strategies | ✅ |
| `resume-assistant` | Resume analysis, tailoring, cover letter workflow | ✅ |
| `job-tracker` | Application tracking pipeline | ✅ |
| `career-research` | Salary benchmarking, company due diligence | ✅ |

### Phase 6 — Workflow Templates
| Template | Steps | Status |
|----------|-------|--------|
| `job-application-prep` | search → company → analyze → tailor → cover letter → tracker | ✅ |
| `resume-audit` | multi-source search → 2x analyze → salary → audit report | ✅ |

---

## Comparison vs AIHawk (29k★)

| Feature | AIHawk | go_job + vaelor-jobs |
|---------|--------|---------------------|
| Job search | LinkedIn + Indeed (Selenium) | 10+ sources, no browser |
| Resume tailoring | ✅ | ✅ |
| Cover letter | ✅ AI-generated | ✅ AI-generated |
| ATS analysis | ❌ | ✅ score + keywords + gaps |
| Salary research | ❌ | ✅ p25/median/p75 |
| Company research | ❌ | ✅ full overview |
| Job tracker | ✅ SQLite | ✅ SQLite |
| Auto-apply | ✅ EasyApply | ❌ (by design) |
| Auth required | ✅ LinkedIn login | ❌ no credentials |
| Browser required | ✅ Selenium | ❌ headless |
| MCP interface | ❌ | ✅ |
| Caching | ❌ | ✅ L1+L2 Redis |
| Language | Python | Go |

**go_job advantages:** no browser, no credentials, MCP-native, caching, more sources, salary+company research, ATS scoring

**AIHawk advantage:** auto-apply (EasyApply) — intentionally not implemented (ToS violation risk)

---

## Roadmap — Next Steps

### High Priority

| Feature | Effort | Notes |
|---------|--------|-------|
| **Glassdoor source** | Medium | Salary data + company reviews via SearXNG |
| **ZipRecruiter** | Medium | Large US market |
| **Google Jobs** | Low | SearXNG `site:jobs.google.com` |
| **Pagination** | Low | `offset` param for LinkedIn Guest API |
| **`results_limit` param** | Low | Currently fixed at ~15 per source |

### Medium Priority

| Feature | Effort | Notes |
|---------|--------|-------|
| **User profile** | Low | `~/.vaelor-jobs/workspace/user-profile.md` — resume, preferences, blacklist |
| **Blacklist filter** | Low | Skip companies/keywords in job_search |
| **Duplicate detection** | Low | Dedup across sources by title+company |
| **Interview prep** | Medium | Generate likely interview questions from JD + company research |
| **Offer comparison** | Low | Compare multiple offers side-by-side |

### Low Priority

| Feature | Effort | Notes |
|---------|--------|-------|
| **Alert/watch mode** | Medium | Periodic re-search + Telegram notify on new matches |
| **PDF resume parsing** | Medium | Extract text from uploaded PDF |
| **LinkedIn profile scrape** | High | Extract experience from LinkedIn profile URL |
| **Salary negotiation script** | Low | LLM-generated negotiation talking points |

---

## Architecture

```
User (Telegram / API)
        │
        ▼
vaelor-orchestrator (port 18790)
        │ A2A
        ▼
vaelor-jobs (port 18796)
  ├── SOUL.md — Career Assistant identity
  ├── skills/
  │   ├── job-search/SKILL.md
  │   ├── resume-assistant/SKILL.md
  │   ├── job-tracker/SKILL.md
  │   └── career-research/SKILL.md
  └── workflows/
      ├── job-application-prep.json
      └── resume-audit.json
        │ MCP
        ▼
go_job MCP server (port 8891)
  ├── job_search          (10+ sources)
  ├── remote_work_search  (RemoteOK, WWR)
  ├── freelance_search    (Upwork, Freelancer)
  ├── resume_analyze      (LLM + ATS scoring)
  ├── cover_letter_generate (LLM)
  ├── resume_tailor       (LLM + diff)
  ├── salary_research     (SearXNG + LLM)
  ├── company_research    (SearXNG + LLM)
  ├── job_tracker_add     (SQLite)
  ├── job_tracker_list    (SQLite)
  └── job_tracker_update  (SQLite)
```

---

## Data Storage

| Store | Location | Purpose |
|-------|----------|---------|
| Job tracker | `~/.go_job/tracker.db` | SQLite, persists across restarts |
| L1 cache | in-memory (sync.Map) | Fast, lost on restart |
| L2 cache | Redis (optional) | Persistent, shared across instances |

---

## Key Design Decisions

1. **No browser automation** — all sources use public APIs, SearXNG, or RSS. No Selenium/Playwright.
2. **No credentials required** — LinkedIn Guest API, public ATS boards, open APIs only.
3. **LLM for intelligence** — resume analysis, cover letters, salary aggregation all use the configured LLM.
4. **Resume as text** — user pastes resume text directly; no PDF parsing needed for agent workflow.
5. **SQLite for tracker** — simple, portable, no external dependencies.
