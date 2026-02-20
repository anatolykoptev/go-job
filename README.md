# go_job

Job, Remote & Freelance Search MCP server.

Exposes three MCP tools:
- **job_search** — LinkedIn, Greenhouse, Lever, YC, HN Who is Hiring
- **remote_work_search** — RemoteOK, WeWorkRemotely + SearXNG
- **freelance_search** — Upwork, Freelancer.com (direct API + SearXNG)

## Architecture

```
go_job/
├── main.go                    # HTTP/stdio MCP server entry point
├── internal/
│   ├── jobserver/register.go  # Tool registrations (job_search, remote_work_search, freelance_search)
│   └── toolutil/toolutil.go   # Shared helpers (cache, fetch, lang)
├── deploy/
│   └── go_job.service         # systemd unit
└── Makefile
```

### Current dependency on go-search

`go_job` currently depends on `github.com/anatolykoptev/go-search` via a `replace` directive
pointing to `../go-search`. This provides:
- `pkg/engine` — cache, search, LLM, types
- `pkg/jobs` — LinkedIn, Greenhouse, Lever, YC, HN, RemoteOK, WWR
- `pkg/sources` — Freelancer.com API

**Decoupling roadmap:**
1. Copy `internal/engine/` from go-search → `go_job/internal/engine/`
2. Update imports in `internal/jobserver/` and `internal/toolutil/`
3. Remove `replace` directive and `go-search` dependency from `go.mod`

## Running

```bash
# HTTP mode (default port 8891)
MCP_PORT=8891 LLM_API_KEY=... ./bin/go_job

# stdio mode (for MCP clients)
./bin/go_job --stdio
```

## Build & Deploy

```bash
make build    # build binary to bin/go_job
make deploy   # build + copy service + restart systemd unit
make restart  # restart only
```

## Health check

```bash
curl http://localhost:8891/health
# {"status":"ok","service":"go_job","version":"1.0.0"}
```
