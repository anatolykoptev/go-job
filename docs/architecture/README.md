# Architecture

Architecture notes and ADRs for go-job.

**Start here: [principles.md](principles.md)** — what the service is for, which decisions
are computed rather than judged, where the durable difference lives, and the target shape
of the search pipeline. It is the tie-breaker: where any other doc in this repo disagrees
with it, the other doc is the bug.

## Data & admin UI (load-bearing principle)

- The **gojob Postgres DB** is the single source of truth for all hunt/resume/admin
  data (`hunt_jobs`, `hunt_bounties`, `hunt_freelance`, `hunt_security`,
  `hunt_audit_contests`, `hunt_ratings`, `resume_*`). Do NOT introduce file-based
  data sources for application data.
- Admin list pages are **go-panel `resource.Resource`** over SQL. The reusable table
  widget — search box, filter chips, sortable headers, pagination, htmx — lives in the
  framework stack and is deliberately shared:
  - **go-kit/admintable** — the engine (`Spec` for sort, `FilterSpec`/`Filter` for
    filter + free-text `ILike` search, pagination math).
  - **go-panel/resource** — the admin framework (`Resource`, `Lister`, `ListQuery`,
    `Cell`/`Row`, `makeListHandler`, `list.templ`, `Detailer`, `RenderPage`) + `shell`.
  - **go-job/internal/adminui** — the consumer: declare a `Resource` (Spec + FilterSpec
    + Lister) and the widget is rendered for free. Never hand-roll a table.
- Consumer-specific cells (fit/market chips, PDF download links) are `Cell{HTML:true}`
  built inside the go-job lister — domain HTML belongs in the consumer, not the framework.
