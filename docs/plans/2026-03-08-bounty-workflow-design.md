# Bounty Workflow Tools — Design

## Goal

Add tools to go-job for the full bounty lifecycle: claim bounties, monitor new ones, and analyze complexity before committing.

## Features

### 1. `bounty_attempt` — Claim a bounty

New MCP tool. Comments `/attempt #N` on a GitHub issue via GitHub API.

- Input: `{url: "https://github.com/org/repo/issues/123"}`
- Parses URL via `ParseGitHubIssueURL()` → owner, repo, number
- `POST /repos/{owner}/{repo}/issues/{number}/comments` with body `/attempt #N`
- Uses existing `GITHUB_TOKEN`
- Returns confirmation + link to comment

### 2. `bounty_monitor` — Background new bounty alerts

Background goroutine polling Algora tRPC API every N minutes.

- Compares current bounty IDs with previously seen set (stored in Redis key `gj:bounty_seen_ids`)
- New bounties → sends Telegram notification via vaelor `message` tool
- Notification format: amount, title, skills, GitHub URL
- Config: `VAELOR_NOTIFY_URL`, `BOUNTY_NOTIFY_CHAT_ID` (default "428660"), `BOUNTY_MONITOR_INTERVAL` (default 15m)

### 3. `bounty_analyze` — Complexity analysis

New MCP tool. Fetches issue body and uses LLM to estimate effort.

- Input: `{url: "https://github.com/org/repo/issues/123"}`
- Fetches issue body from Algora tRPC API cache (already has `task.body`) or GitHub API fallback
- LLM prompt: estimate complexity (1-5), hours, required skills, $/hour
- Returns structured analysis to help decide whether to take the bounty

## Architecture

All three features live in go-job under `internal/jobserver/` (tool registration) and `internal/engine/jobs/` (logic). The monitor goroutine starts in `main.go` after engine init.

Vaelor integration: simple HTTP POST to vaelor's tool execution endpoint for Telegram notifications.
