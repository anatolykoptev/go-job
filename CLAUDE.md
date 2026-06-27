# go-job — agent rules

- Data source of truth is the gojob Postgres DB. Admin list pages MUST be a go-panel `resource.Resource` over SQL so they reuse the go-kit/admintable table widget (search + filter + sort + pagination + htmx) — never invent file-based data sources (e.g. `_tracker.json`) and never hand-roll bespoke HTML tables.
