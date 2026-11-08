# go-job — agent rules

- **Public repo.** go-job is public; everything committed must be generic and reusable by anyone. No operator-specific data, paths, hostnames, accounts, or single-user assumptions in code; personal/instance data (resumes, profiles, tokens, chat IDs) lives in the DB / env / config, never committed; defaults, examples, and seeds stay neutral, not one operator's.
- Data source of truth is the gojob Postgres DB. Admin list pages MUST be a go-panel `resource.Resource` over SQL so they reuse the go-kit/admintable table widget (search + filter + sort + pagination + htmx) — never invent file-based data sources (e.g. `_tracker.json`) and never hand-roll bespoke HTML tables.

Every document is indexed in [docs/INDEX.md](docs/INDEX.md). Contribution rules: [CONTRIBUTING.md](CONTRIBUTING.md).

## Layout

`main.go` wires the MCP server (`MCP_PORT`, `--stdio`), the metrics port and the go-panel admin server
(`ADMIN_PORT`). `internal/`: `jobserver` (MCP tool handlers, one `tool_*.go` per tool, registered in
`register.go`), `engine` (config, cache, search, `jobs/` source and career-tool implementations,
`sources/` connectors), `hunt` (hunt store, scoring, enrichment, notifications), `huntworker`,
`accounts` (accounts, API keys, bootstrap), `adminui` (operator UI), `oversize` (spillover store),
`pdfrender`, `resumeimport`, `quality`, `redact`, `dbtest`. `cmd/` holds one-off migration tools and
`gojob-admin`. `vendor/` is tracked.

## Gate

CI (`.github/workflows/preflight.yml`, `ubuntu-24.04-arm`, a pgvector Postgres 16 service, `GOWORK=off`)
applies the schema files and then runs:

```sh
make preflight   # fitness greps, go vet ./internal/..., go test -p $GO_TEST_PARALLEL ./internal/...
```

`GO_TEST_PARALLEL` is 1 in CI and by default; with `DATABASE_URL` set the DB round-trip tests run
live, without it they skip. `make lint` runs golangci-lint; `make mutation` runs gremlins on the PR
diff (the `mutation` workflow does it on pull requests). A docs-only PR (Markdown, `docs/`,
licence, templates) skips the heavy job through the `docs-scope` job in `preflight.yml`; that block
is canonical across the fleet and compared byte for byte, so do not edit it here. Versions come from
release-please (Conventional Commits); never tag or edit `CHANGELOG.md` by hand.

## Invariants that are easy to break

`make preflight` greps for most of these and fails with an explanation; read its `FAIL:` messages
before working around one.

- **Account scoping.** Identity comes from the verified session or bearer key (`acctOf`,
  `accounts.AccountFrom`, `store.ForAccount`), never from `tenant.From`, the `X-MCP-User` header,
  `adminUser` or a hard-coded account literal. Score columns live in `account_job_scores`, not on
  `hunt_jobs`.
- **Schema ownership.** Account-owned tables (`account_job_scores`, `account_hunt_settings`,
  `mcp_api_keys`) are `go:embed`ded and applied by `accounts.Bootstrap`, never as migration-root files;
  Bootstrap order is covered by `accounts.TestBootstrap_Order_SourceGate`.
- **Admin auth.** No `RequiredRole` on adminui resources (it panics under `AUTH_DRIVER=hmac`);
  self-serve routes mount only inside `if d.selfServe`, which is true for the bcrypt driver only.
- **One CSS source.** Copy-block and chip CSS lives only in `adminui/partials.go`; money parsing uses
  `parseDollarsToCents` and `centsToDollars`, not inline math.
- **Release builds carry the version through `git describe`**, never a hand-edited number.
- **A tracked 53 MB `go_job` binary sits in the repository root**; do not rebuild into the root, use
  `make build` (output in `bin/`).
