# Design Principles

> Last updated: 2026-07-31 · Basis: `main@d039007`
>
> This is the canonical statement of what go-job is for and how decisions in it are
> made. Where any other document in this repo disagrees with this one, this one wins
> and the other document is a bug.

## 1. The criterion is what the model is fed, not whether a model is used

A model in the codebase is not a problem. Feeding it commodity input is.

If the LLM's input is something a user could paste into a chat window — a query plus
ten job descriptions — then this service is substituting for a chat, more slowly and
at higher cost, and it loses. If the input is something only this service holds (the
user's accumulated rejections with reasons, real interview outcomes, resume claims
traced to recorded facts) or is at a scale nobody would do by hand (500 listings a
night, each read carefully), the model is a source of uniqueness.

Every LLM call site in this repo should be readable against that test. Several
currently fail it — see [§6](#6-where-we-actually-are).

## 2. Decisions that can be computed should be computed

Not for purity. Where the answer is computed it can be tested, mutated and
regression-guarded. Where it is a model's opinion there is nothing to assert.

The precedent already exists in this codebase: `JobListing.QualityScore` is documented
in `internal/engine/types_jobs.go:41` as a "0-100 deterministic posting-quality score
(no LLM)". That is the shape every deciding number should have.

The corollary is uncomfortable and worth stating plainly: the one path in this service
that is not mechanical is also the only one that has been broken in production
(zero results returned on five consecutive live queries, 2026-07-31). That is not a
coincidence — it is what "nothing to assert" costs.

## 3. A cross-encoder is not mechanization

Substituting a smaller opaque model for a larger opaque model does not satisfy §2, even
though it is the retrieval industry's default answer. A cross-encoder score cannot show
which term contributed what, and its range is model-dependent (`mxbai` normalises
against an empirical `estimated_max=9.0`), so a threshold on it is a judgement wearing a
number's clothes.

Use a cross-encoder for **ordering** if at all. Never as the auditable layer, and never
as "the mechanical fix".

## 4. The moat, ranked by durability

1. **The longitudinal record.** `hunt_jobs`, tracker stages, `application_persist`,
   `resume_memory`, `resume_vectors`. Which listings the user already saw and rejected,
   where they applied, on what date, at what stage, which resume variant went out. Months
   of accumulation, cannot be pasted into a prompt, grows rather than depreciating.
2. **Provenance of claims about the user.** `master_resume` with `is_master`, `parent_id`
   and atomic promotion is a provenance system. A chat model will write "led a team of 8"
   because it is plausible; this service can require every claim to trace to a recorded
   fact. That is the difference between a cover letter and a defensible one.
3. **Determinism you can audit.** A ranked list that shows its arithmetic is a product.
   "The model thought these look promising" is not — the user can get that opinion free
   elsewhere and cannot check it either way.
4. **Continuous operation.** A posting appears at 03:00, is scored, and notifies if it
   clears the bar. Chat is pull; this is push.

**Scraping is explicitly not on this list.** It is a depreciating asset: anti-bot bypass
is an arms race won by infrastructure scale, and some boards will open APIs anyway.
Useful today, not an identity. Any document that frames scraping, TLS fingerprinting or
"no auth required" as *the* differentiator is wrong.

## 5. The search architecture

Supersedes every earlier description of the `job_search` pipeline.

```
sources ──► collect (unchanged)
    │
    ▼
PASS 1 — MECHANICAL (no model)
  · BM25 over title / company / skills / description
  · embeddings as a second signal for synonymy
  · fused with RRF — ranks, not scores, so no cross-scale calibration
  · already exists in go-kit/rerank, proven in MemDB
  Purpose: candidate selection, and an always-showable auditable layer.
    │
    ▼
SELECT top-K by rank                     ← output size bounded HERE
    │
    ▼
PASS 2 — MODEL, over the top-K, WITH the user's history as context
  Its question is NOT "is this relevant to the query" — a chat window does that.
  It is "given they rejected six like this in March and converted twice on that,
  what does this one look like".
    │
    ▼
OUTPUT — both layers shown
  · the mechanical score, with per-term contributions
  · the history-grounded judgement, marked as such
```

Rules that follow from it:

- Thresholds are measured numbers read off traffic histograms, never judgements.
- Skills and seniority come from a taxonomy match, not a model.
- Every **deciding** line leaves the prompts. Concretely, at `main@d039007`:
  `ONLY jobs relevant to the query keywords` in `JobSearchInstruction`
  (`internal/engine/prompt_jobs.go:33`) and its bounty twin (`:99`); and
  `which … look most promising` in the four summary contracts (`:29`, `:65`, `:95`,
  `:125`). Deleted, not softened.
- Structured sources are never round-tripped through the model. `ats.go` emits typed
  `JobListing` values and `ApplyStructuredPrecedence` (`internal/engine/jobs/ats.go:1349`)
  merges them field-by-field, additively (#418).

## 6. Where we actually are

Measured at `main@d039007`, because the honest number matters more than the flattering
one:

| Probe | Count |
|---|---|
| `engine.CallLLM` call sites under `internal/` (non-test) | **18**, in 14 files |
| Files: `algora_analyze.go`, `interview.go`, `negotiation.go`, `offer.go`, `person.go`, `pitch.go`, `research.go` (×2), `resume.go` (×3), `resume_enrich.go` (×2), `resume_gen.go` (×2), `showcase.go`, `skillgap.go`, `sources/hackernews.go` | |
| Plus the search/extraction summarisers | `bridge_jobs.go`, `bridge_llm.go`, `algora_llm.go`, `remotejobs.go` |
| Job-domain instruction constants | **5** (`JobSearchInstruction`, `LinkedInJobsInstruction` — an alias of it — `FreelanceSearchInstruction`, `BountySearchInstruction`, `RemoteWorkInstruction`), all of them search |
| Non-job instruction constants inherited from the go-search lineage | 8, in `internal/engine/prompt.go` |

So the LLM is **not** confined to search and extraction. `interview_prep`,
`pitch_generate`, `offer_compare`, `skill_gap` and `resume_generate` each reach it
through a thin MCP wrapper over `internal/engine/jobs/`. By §1 those calls are fed
commodity input — a resume plus a job description, exactly what a user could paste into
a chat — so they are the weakest surface in the product, not the strongest.

That does not mean cut them. It means they are the backlog: each one either graduates to
history-grounded input (§4.1, §4.2) or it is a convenience feature that competes with a
free chat window, and should be described that way.

## 7. The differentiator to build next

Ranking by query text is table stakes. Ranking by the user's own accumulated history is
not: what they rejected and how fast, where they reached final rounds, which stated
requirements turned out not to be blockers, which companies went quiet. No competitor has
that signal, no chat session has it, and it strengthens every month the service runs.

It is downstream of moat asset #1, which is why the mechanical ranking layer (§5, pass 1)
lands first — so there is something honest to feed the history signal into.
