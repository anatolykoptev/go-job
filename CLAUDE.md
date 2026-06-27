# go-job — agent rules

- **Public repo.** go-job is public; everything committed must be generic and reusable by anyone. No operator-specific data, paths, hostnames, accounts, or single-user assumptions in code; personal/instance data (resumes, profiles, tokens, chat IDs) lives in the DB / env / config, never committed; defaults, examples, and seeds stay neutral, not one operator's.
- Data source of truth is the gojob Postgres DB. Admin list pages MUST be a go-panel `resource.Resource` over SQL so they reuse the go-kit/admintable table widget (search + filter + sort + pagination + htmx) — never invent file-based data sources (e.g. `_tracker.json`) and never hand-roll bespoke HTML tables.
