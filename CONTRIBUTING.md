# Contributing to go-job

go-job is a Go MCP server; changes are welcome when they keep tool names and arguments stable and
are covered by tests. Keep everything generic: no operator-specific data, paths or hostnames.

## Development

Requires Go 1.26, golangci-lint and, for the database tests, a PostgreSQL 16 with pgvector.

```sh
make build        # GOWORK=off go build -o bin/go_job .
make lint         # GOWORK=off golangci-lint run ./...
make preflight    # fitness checks, go vet ./internal/..., go test ./internal/...
make mutation     # gremlins mutation testing on the diff against origin/main
```

`make preflight` is the merge gate and CI runs exactly it (`.github/workflows/preflight.yml`) after
applying the schema files to a Postgres service. Set `DATABASE_URL` to a throwaway database to run
the DB round-trip tests live; without it they skip. `GO_TEST_PARALLEL` (default 1) caps test
parallelism. A pull request that changes only documents skips the heavy job.

Dependencies are vendored: after changing `go.mod` run `go mod vendor` and commit `vendor/`.

## Commits

Conventional Commits (`feat:`, `fix:`, `perf:`, `refactor:`, `docs:`, `deps:`, `chore:`, `ci:`,
`test:`). release-please reads them to cut versions and write [CHANGELOG.md](CHANGELOG.md); do not
tag or edit the changelog by hand.

## Pull requests

- Branch from `main`, keep the change focused, and fill in the pull-request template.
- Add tests for behaviour changes; the fitness checks in `make preflight` explain the rules they enforce.
- Update [docs/INDEX.md](docs/INDEX.md) when you add or rename a document.
- Never commit resumes, profiles, tokens or other personal data.

## Reporting problems

Bugs and feature requests go through the issue templates. Report vulnerabilities privately as
described in [SECURITY.md](SECURITY.md).
