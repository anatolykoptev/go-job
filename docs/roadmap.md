# go_job + vaelor-jobs — Career Assistant Roadmap

> AIHawk-level career assistant through vaelor-jobs agent + go_job MCP server.
> Last updated: 2026-02-28

---

## Vision

Full career pipeline through a single AI agent (vaelor-jobs):

```
Find Jobs → Research → Prepare Application → Interview Prep → Track Pipeline → Negotiate Offer
```

No browser automation. No credentials. Pure API + LLM.

---

## Implemented ✅

### Phase 1 — Job Search (go_job v1.0)
| Tool | Sources | Status |
|------|---------|--------|
| `job_search` | LinkedIn Guest API, Greenhouse, Lever, YC, HN, Indeed, Хабр Карьера, **Twitter/X** | ✅ |
| `remote_work_search` | RemoteOK API, WeWorkRemotely RSS, Remotive API | ✅ |
| `freelance_search` | Freelancer.com REST API, Upwork SearXNG | ✅ |
| `twitter_job_search` | Twitter/X GraphQL via go-twitter (raw tweets, no LLM) | ✅ |
| `job_match_score` | Jaccard keyword overlap: resume vs job listings (0-100) | ✅ |

**Filters:** experience, job_type, remote, time_range, salary (LinkedIn f_SB2), platform (incl. twitter), location

**Sources (11):** LinkedIn, Greenhouse, Lever, YC, HN, Indeed, Хабр, RemoteOK, WeWorkRemotely, Remotive, Twitter/X

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
| `person_research` | Hiring manager background from LinkedIn, GitHub, Twitter, Habr, web | ✅ |

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

### Phase 7 — Interview Preparation (go_job v1.2)
| Tool | Description | Status |
|------|-------------|--------|
| `interview_prep` | Personalized Q&A (behavioral + technical + system design) with model answers from resume, optional company enrichment | ✅ |
| `project_showcase` | STAR-format project narratives with impact and talking points | ✅ |
| `pitch_generate` | 30-sec & 2-min elevator pitches, "why this company" answer, optional company enrichment | ✅ |
| `skill_gap` | Resume vs JD gap analysis: match score, missing skills with priority/learning time, learning plan | ✅ |

### Phase 8 — Application Workflow (go_job v1.2)
| Tool | Description | Status |
|------|-------------|--------|
| `application_prep` | One-call combo: resume analysis + cover letter + interview prep + company research (parallel execution) | ✅ |
| `offer_compare` | Side-by-side offer comparison with scoring (0-100) and recommendation | ✅ |
| `negotiation_prep` | Salary negotiation playbook: scripts, counters, BATNA, red flags, optional salary research enrichment | ✅ |

**Total: 25 MCP tools, 11 job sources, 6 vaelor skills/workflows**

---

## Comparison vs Market

### vs AIHawk (29k★)

| Feature | AIHawk | go_job + vaelor-jobs |
|---------|--------|---------------------|
| Job search | LinkedIn + Indeed (Selenium) | 11 sources, no browser |
| Resume tailoring | ✅ | ✅ |
| Cover letter | ✅ AI-generated | ✅ AI-generated |
| ATS analysis | ❌ | ✅ score + keywords + gaps |
| Salary research | ❌ | ✅ p25/median/p75 |
| Company research | ❌ | ✅ full overview |
| Person research | ❌ | ✅ hiring manager background |
| Job tracker | ✅ SQLite | ✅ SQLite |
| Resume match score | ❌ | ✅ Jaccard (0-100) |
| Twitter/X search | ❌ | ✅ raw tweets + pipeline |
| Auto-apply | ✅ EasyApply | ❌ (by design) |
| Interview prep | ❌ | ✅ Q&A + STAR showcase + pitches + skill gap |
| Offer comparison | ❌ | ✅ side-by-side scoring |
| Salary negotiation | ❌ | ✅ scripts + BATNA |
| Auth required | ✅ LinkedIn login | ❌ no credentials |
| Browser required | ✅ Selenium | ❌ headless |
| MCP interface | ❌ | ✅ |
| Caching | ❌ | ✅ L1+L2 Redis |
| Language | Python | Go |

**go_job advantages:** no browser, no credentials, MCP-native, caching, 11 sources, salary+company+person research, ATS scoring, Twitter/X

**AIHawk advantage:** auto-apply (EasyApply) — intentionally not implemented (ToS violation risk)

### vs Commercial Tools

| Feature | JobCopilot ($29/mo) | AIApply | FinalRound AI | go_job |
|---------|---------------------|---------|---------------|--------|
| Job search | ✅ | ✅ | ❌ | ✅ 11 sources |
| Auto-apply | ✅ | ✅ | ❌ | ❌ by design |
| Resume builder | ✅ | ✅ ATS-optimized | ❌ | ✅ analyze+tailor |
| Cover letter | ✅ | ✅ | ❌ | ✅ 3 tones |
| Interview prep | ❌ | ❌ | ✅ mock interviews | ✅ Q&A + pitches + STAR |
| Offer negotiation | ❌ | ❌ | ❌ | ✅ scripts + BATNA |
| Live interview coaching | ❌ | ✅ Interview Buddy | ✅ | 🔜 Phase 9 |
| Company research | ❌ | ❌ | ❌ | ✅ |
| Salary research | ❌ | ❌ | ❌ | ✅ |
| Self-hosted | ❌ | ❌ | ❌ | ✅ |
| Price | $29/mo | paid | paid | free |

---

## Roadmap — Next Steps

### Phase 9 — Advanced Interview (LOW PRIORITY, HIGH IMPACT)

> Beyond Q&A generation — interactive practice and live coaching.

| Feature | Tool/Skill | Effort | Notes |
|---------|------------|--------|-------|
| **Mock interview session** | vaelor skill | High | Multi-turn conversation simulating real interview. Interviewer persona based on person_research of actual hiring manager. Feedback after each answer (clarity, depth, STAR compliance). |
| **System design practice** | vaelor skill | High | Interactive system design session: interviewer asks, candidate draws (text-based), interviewer probes. Tailored to company's tech stack (from company_research). |
| **Live interview companion** | vaelor skill | Medium | Real-time answer suggestions during actual interview. User sends question text → instant structured answer with talking points from their projects. Like AIApply's "Interview Buddy". |

### Phase 10 — More Sources & UX

| Feature | Effort | Notes |
|---------|--------|-------|
| **Glassdoor source** | Medium | Salary data + company reviews via SearXNG |
| **ZipRecruiter** | Medium | Large US market |
| **Google Jobs** | Low | SearXNG `site:jobs.google.com` |
| **Pagination** | Low | `offset` param for LinkedIn Guest API |
| **`results_limit` param** | Low | Currently fixed at ~15 per source |
| **User profile** | Low | `~/.go_job/profile.md` — resume, preferences, blacklist |
| **Blacklist filter** | Low | Skip companies/keywords in job_search |
| **Alert/watch mode** | Medium | Periodic re-search + Telegram notify on new matches |
| **PDF resume parsing** | Medium | Extract text from uploaded PDF |
| **LinkedIn profile scrape** | High | Extract experience from LinkedIn profile URL |

---

## Architecture

```
User (Telegram / Claude Code / API)
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
  │   ├── career-research/SKILL.md
  │   ├── interview-prep/SKILL.md        ← Phase 7
  │   └── mock-interview/SKILL.md        ← Phase 9
  └── workflows/
      ├── job-application-prep.json
      ├── resume-audit.json
      └── full-application-package.json   ← Phase 8
        │ MCP
        ▼
go_job MCP server (port 8891, 25 tools)
  ├── job_search            (11 sources incl. Twitter/X)
  ├── remote_work_search    (RemoteOK, WWR, Remotive)
  ├── freelance_search      (Upwork, Freelancer)
  ├── twitter_job_search    (raw tweets via go-twitter)
  ├── job_match_score       (Jaccard resume↔job)
  ├── resume_analyze        (LLM + ATS scoring)
  ├── cover_letter_generate (LLM, 3 tones)
  ├── resume_tailor         (LLM + keyword diff)
  ├── salary_research       (SearXNG + LLM)
  ├── company_research      (SearXNG + LLM)
  ├── person_research       (LinkedIn + GitHub + Twitter + web)
  ├── job_tracker_add       (SQLite)
  ├── job_tracker_list      (SQLite)
  ├── job_tracker_update    (SQLite)
  ├── interview_prep        (LLM + company enrichment)
  ├── project_showcase      (LLM, STAR format)
  ├── pitch_generate        (LLM + company enrichment)
  ├── skill_gap             (keyword matching + LLM)
  ├── application_prep      (parallel: analyze + cover + interview + company)
  ├── offer_compare         (LLM, scoring 0-100)
  ├── negotiation_prep      (LLM + salary research)
  ├── master_resume_build   (LLM, master profile)
  ├── resume_generate       (LLM from master profile)
  ├── resume_enrich         (LLM, Q&A enrichment)
  ├── resume_profile        (master profile viewer)
  ├── resume_memory_search  (semantic search)
  ├── resume_memory_add     (memory store)
  └── resume_memory_update  (memory update)
```

---

## Data Storage

| Store | Location | Purpose |
|-------|----------|---------|
| Job tracker | `~/.go_job/tracker.db` | SQLite, persists across restarts |
| L1 cache | in-memory (sync.Map) | Fast, lost on restart |
| L2 cache | Redis (optional) | Persistent, shared across instances |
| User profile | `~/.go_job/profile.md` | Resume, preferences, blacklist (Phase 10) |

---

## Key Design Decisions

1. **No browser automation** — all sources use public APIs, SearXNG, or RSS. No Selenium/Playwright.
2. **No credentials required** — LinkedIn Guest API, public ATS boards, open APIs only. Twitter via go-twitter (open accounts fallback).
3. **LLM for intelligence** — resume analysis, cover letters, salary aggregation, interview prep all use the configured LLM.
4. **Resume as text** — user pastes resume text directly; no PDF parsing needed for agent workflow.
5. **SQLite for tracker** — simple, portable, no external dependencies.
6. **Interview prep over auto-apply** — auto-apply is risky (ToS) and low-signal. Interview preparation has higher ROI for candidates with non-traditional backgrounds.
