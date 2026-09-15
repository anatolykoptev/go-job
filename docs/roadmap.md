# go_job — Career Assistant Roadmap

> AIHawk-level career assistant through a Claude Code / MCP agent + go_job MCP server.
> Last updated: 2026-07-31

---

## Vision

Full career pipeline through a single AI agent:

```
Find Jobs → Research → Prepare Application → Interview Prep → Track Pipeline → Negotiate Offer
```

No browser automation. No credentials — public APIs, SearXNG and RSS only.

Those are engineering choices, not the product's edge. The edge is the accumulated
record and the auditability of the decisions made over it — see
[`architecture/principles.md`](architecture/principles.md).

---

## Implemented ✅

### Phase 1 — Job Search (go_job v1.0)
| Tool | Sources | Status |
|------|---------|--------|
| `job_search` | LinkedIn Guest API, Greenhouse, Lever, YC, HN, Indeed, Хабр Карьера, Twitter/X (platform=twitter), Google Jobs (platform=google) | ✅ |
| `job_match_score` | Jaccard keyword overlap: resume vs job listings (0-100) | ✅ |

**Filters:** experience, job_type, remote, time_range, salary (LinkedIn f_SB2), platform, location, limit, offset, blacklist

**Sources (15+):** LinkedIn, Greenhouse, Lever, YC, HN, Indeed, Хабр, RemoteOK, WeWorkRemotely, Remotive, Twitter/X, Google Jobs, Inspira (UN), UNDP, Freelancer.com

### Phase 2 — Resume & Cover Letter (go_job v1.1)
| Tool | Description | Status |
|------|-------------|--------|
| `resume_analyze` | ATS score (0-100), missing keywords, gaps, recommendations | ✅ |
| `cover_letter_generate` | Tailored cover letter (professional/friendly/concise) | ✅ |
| `resume_tailor` | Rewrite resume sections to match JD, keyword diff | ✅ |

### Phase 3 — Research (go_job v1.1)
| Tool | Description | Status |
|------|-------------|--------|
| `research` | Single consolidated tool. subject=salary — p25/median/p75; subject=company — size, funding, tech stack, culture; subject=person — hiring manager background | ✅ |

### Phase 4 — Job Tracker (go_job v1.1)
| Tool | Description | Status |
|------|-------------|--------|
| `job_tracker` | Single tool: action=add (save job), action=list (filter by status), action=update (update status/notes by ID). Storage: $UPLOADS_ROOT/go-job/tracker/tracker.db (SQLite) | ✅ |

### Phase 5 — Agent Skills
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

### Phase 10a — Search UX Improvements (go_job v1.3)
| Feature | Description | Status |
|---------|-------------|--------|
| `results_limit` | `limit` param on `job_search` (default 15, max 50) | ✅ |
| `pagination` | `offset` param — skip N results for pagination | ✅ |
| `blacklist` | Comma-separated company/keyword exclusion filter | ✅ |
| `google_jobs` | Google Jobs source via SearXNG (platform=google) | ✅ |
| `user_profile` | Default platform, limit, location, remote, blacklist stored at $UPLOADS_ROOT/go-job/profile/profile.json | ✅ |

**Filters (updated):** experience, job_type, remote, time_range, salary, platform (incl. twitter, google, inspira, undp, un), location, limit, offset, blacklist

**Total: 32 MCP tools, 17 source connectors**

---

## Comparison vs Market

### vs AIHawk (29k★)

| Feature | AIHawk | go_job |
|---------|--------|--------|
| Job search | LinkedIn + Indeed (Selenium) | 15+ sources, no browser |
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

---

## Roadmap — Next Steps

### Phase 11 — Multi-User (HIGH PRIORITY)

> **Why now:** go_job was built for one operator. Prospective users outside that
> scope now want to run it, and every one of them would share the same profile,
> the same tracker and the same generated documents.
>
> **Status:** architecture settled (ADRs A–J, reviewed 2026-06-27). Implementation
> not started as of 2026-07-30.

Single-user is not a missing feature — it is an assumption that is load-bearing in
several places. Each row below is a place where "the user" is currently implicit.

| Area | Today's single-user assumption | Target |
|------|-------------------------------|--------|
| Resume profile | `GetLatestPersonID` returns the highest `resume_persons.id`, with no ownership condition | account-scoped lookup; a person belongs to exactly one account |
| Job scoring | per-user fit columns live on the shared `hunt_jobs` corpus rows | per-account `account_job_scores`; no shared-corpus table carries a per-account column |
| Application artifacts | `applications/<job_id>/` is keyed by job alone, so two users applying to one job collide | per-account namespacing plus an ownership check on read |
| MCP surface | one shared authority, no identity on the tool transport | account resolved once at the transport choke point, inherited by every tool |
| Accounts | single operator, HMAC admin | bcrypt accounts and public self-service registration |
| Destructive ops | `ClearAllPersons` issues an unqualified `DELETE` | scoped by construction; an unscoped statement unreachable through the store API |

**Isolation model.** An `account_id` FK on per-user tables. The crawl corpus
(`hunt_jobs` and its siblings) stays shared and deduped — that is the economics of
crawling — so the per-user judgment written onto those rows moves off into
account-keyed child tables. A shared row is the leak surface; keeping it shared
obligates the partition.

**Enforcement is fail-closed.** A session that resolves to no account gets no data,
never a default workspace. The failure mode to design against is not an error, it
is a silent read of someone else's rows.

**Migration** is expand / backfill / constrain, with the restore rehearsed before
the `NOT NULL` flip. That flip is the one-way door; everything before it is
reversible.

### Phase 12 — Mechanical ranking, then history-grounded judgement (HIGH PRIORITY)

> **Why:** five live queries on 2026-07-31 returned zero jobs each. The mechanism was not
> a bug in a ranking function — there was no ranking function. The model was asked both to
> decide relevance and to reprint the candidate set, so a *successful* search overflowed the
> token cap and a *failed* one parsed cleanly. The only outcome that survived the pipeline
> was failure.

The target pipeline is specified in
[`architecture/principles.md` §5](architecture/principles.md#5-the-search-architecture).
Summary of the work, in order:

| Step | Change | Acceptance |
|---|---|---|
| A ✅ | ATS connectors emit typed `JobListing`; `ApplyStructuredPrecedence` merges additively | A Lever posting with `salaryRange` returns numeric `salary_min`/`salary_max`, not `"not specified"` (#418, merged) |
| B | The remaining connectors do the same | No structured source round-trips its own fields through the model |
| C | Pass 1 — BM25 + embeddings fused with RRF over the collected set, using `go-kit/rerank` | The ranked list is reproducible from the inputs and shows per-term contributions |
| D | Every deciding line leaves the prompts (`ONLY jobs relevant to the query keywords`; `which … look most promising`) — deleted, not softened | No prompt in `prompt_jobs.go` contains an accept/reject rule |
| E | Pass 2 — the model runs over the top-K **with the user's history as context**, answering "given what they rejected and converted on, what is this one" | The judgement is unreproducible in a chat window, and is labelled separately from the mechanical score |
| F | Thresholds set from traffic histograms; skills/seniority from a taxonomy match | Every threshold in the pipeline traces to a measured distribution |

**A cross-encoder is not the mechanical fix.** It is a smaller opaque model taking the same
decision, and it cannot show its arithmetic. Use it for ordering or not at all — see
principles §3. Earlier plans that recommended one as the fix are superseded.

Related issues: #406 (the disabled `JOB_SEARCH_MIN_RELEVANCE` gate — resolve with the
measured verdict, the cosine band was 0.031 wide), #405, #414, #428, #379, #336.

### Phase 9 — Advanced Interview (LOW PRIORITY, HIGH IMPACT)

> Beyond Q&A generation — interactive practice and live coaching.

| Feature | Tool/Skill | Effort | Notes |
|---------|------------|--------|-------|
| **Mock interview session** | Claude Code skill | High | Multi-turn conversation simulating real interview. Interviewer persona based on person_research of actual hiring manager. Feedback after each answer (clarity, depth, STAR compliance). |
| **System design practice** | Claude Code skill | High | Interactive system design session: interviewer asks, candidate draws (text-based), interviewer probes. Tailored to company's tech stack (from company_research). |
| **Live interview companion** | Claude Code skill | Medium | Real-time answer suggestions during actual interview. User sends question text → instant structured answer with talking points from their projects. Like AIApply's "Interview Buddy". |

### Phase 10b — More Sources & UX (remaining)

| Feature | Effort | Notes |
|---------|--------|-------|
| **Glassdoor source** | Medium | Salary data + company reviews via SearXNG |
| **ZipRecruiter** | Medium | Large US market |
| **PDF resume parsing** | Medium | Extract text from uploaded PDF |
| **LinkedIn profile scrape** | High | Extract experience from LinkedIn profile URL |

---

## Architecture

```
User (Telegram / Claude Code / API)
        |
        v
go_job MCP server (port 8891, 32 tools)
  +-- job_search            (15+ sources: LinkedIn, Greenhouse, Lever, YC, HN,
  |                          Indeed, Habr, RemoteOK, WWR, Remotive, Twitter/X,
  |                          Google Jobs, Inspira, UNDP, Freelancer; limit/offset/blacklist)
  +-- job_match_score       (Jaccard resume <-> job)
  +-- opportunity_search    (cross-type: jobs + freelance + bounty)
  +-- opportunity_analyze
  +-- opportunity_claim
  +-- ats                   (direct ATS board fetch)
  +-- resume_analyze        (LLM + ATS scoring)
  +-- cover_letter_generate (LLM, 3 tones)
  +-- resume_tailor         (LLM + keyword diff)
  +-- master_resume_build
  +-- resume_generate
  +-- resume_enrich
  +-- resume_profile
  +-- resume_memory
  +-- research              (subject=salary|company|person; SearXNG + LLM)
  +-- interview_prep        (LLM + company enrichment)
  +-- project_showcase      (LLM, STAR format)
  +-- pitch_generate        (LLM + company enrichment)
  +-- skill_gap             (keyword matching + LLM)
  +-- application_prep      (parallel: analyze + cover + interview + company)
  +-- offer_compare         (LLM, scoring 0-100)
  +-- negotiation_prep      (LLM + salary research)
  +-- linkedin
  +-- linkedin_profile_ingest
  +-- job_tracker           (action=add|list|update; SQLite at UPLOADS_ROOT)
  +-- algora_job_ingest
  +-- hunt_list
  +-- oversize
        |
        v Telegram notifications
internal/hunt/notify/telegram.go
  (go-kit ProductSink, own bot, rate-limited fan-out)
  Requires: TELEGRAM_BOT_TOKEN + HUNT_NOTIFY_CHAT_ID
```

---

## Data Storage

| Store | Location | Purpose |
|-------|----------|---------|
| Job tracker | Postgres `hunt_jobs` + `hunt_ratings` via `DATABASE_URL` | Single source of truth for tracked jobs ([ADR-002](adr/ADR-go-job-002-retire-sqlite-tracker.md)) |
| User profile | `$UPLOADS_ROOT/go-job/profile/profile.json` (default `$HOME/uploads/go-job/profile/profile.json`) | Default platform, limit, location, remote, blacklist |
| L1 cache | in-memory (sync.Map) | Fast, lost on restart |
| L2 cache | Redis (optional) | Persistent, shared across instances |

---

## Key Design Decisions

1. **No browser automation** — all sources use public APIs, SearXNG, or RSS. No Selenium/Playwright.
2. **No credentials required** — LinkedIn Guest API, public ATS boards, open APIs only. Twitter via go-twitter (open accounts fallback).
3. **Compute what can be computed; the model earns its keep on what cannot.**
   `job_quality_score` and `job_match_score` are deterministic by construction, and
   `JobListing.QualityScore` is documented in code as "0-100 deterministic
   posting-quality score (no LLM)" — that is the target shape for every deciding number.
   Measured at `main@d039007`, the LLM is still reached from 18 call sites across 14
   files, including `interview_prep`, `pitch_generate`, `offer_compare`, `skill_gap` and
   `resume_generate`. Those are fed a resume plus a job description — input a user could
   paste into a chat window — so they are the weakest surface, not the strongest, and
   the backlog is to feed them the user's own history instead. Detail and the full
   inventory: [`architecture/principles.md`](architecture/principles.md).
4. **Resume as text** — user pastes resume text directly; no PDF parsing needed for agent workflow.
5. **One store for tracked jobs** — Postgres `hunt_jobs` + `hunt_ratings`. The original
   SQLite `tracker.db` was retired by [ADR-002](adr/ADR-go-job-002-retire-sqlite-tracker.md);
   two stores for one domain produced divergent IDs and unlinkable `fit_score`s. SQLite
   survives only inside the one-shot `cmd/migrate-tracker`.
6. **Interview prep over auto-apply** — auto-apply is risky (ToS) and low-signal. Interview preparation has higher ROI for candidates with non-traditional backgrounds.
7. **Single-user by construction** — one profile, one tracker, one document store; identity is implicit everywhere rather than passed. This was the right call for a personal tool and is the assumption Phase 11 removes. Read it as a scope boundary, not as an architecture that happens to have one user in it.
