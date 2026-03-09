# Opire Integration Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add Opire (opire.dev) as a second bounty source alongside Algora — 0% commission for developers.

**Architecture:** Opire is a Next.js SPA. Data is fetched via RSC endpoint (`Accept: text/x-component`, `RSC: 1` headers) which returns `initialRewards` JSON array inline. We parse this array, map to `BountyListing` with `source: "opire"`, and merge into existing bounty_search and bounty_monitor pipelines.

**Tech Stack:** Go, HTTP client, JSON parsing, Next.js RSC protocol

---

## Context for the implementer

**Project:** `/home/krolik/src/go-job` — Go MCP server for job/bounty search.

**Opire RSC endpoint:**
```bash
curl -s 'https://app.opire.dev/home' \
  -H 'Accept: text/x-component' -H 'RSC: 1' -H 'User-Agent: Mozilla/5.0'
```

Response contains `"initialRewards":[{...},...]` with fields:
- `id` (string), `title` (string), `url` (string — GitHub issue URL)
- `pendingPrice.value` (int — cents), `pendingPrice.unit` ("USD_CENT")
- `programmingLanguages` ([]string), `organization.name` (string)
- `createdAt` (int64 — unix ms), `claimerUsers` / `tryingUsers` (arrays)

**Key existing code:**
- `internal/engine/jobs/algora_api.go` — pattern to follow for API fetch
- `internal/engine/jobs/algora.go:44` — `SearchAlgora()` with cache pattern
- `internal/jobserver/tool_bounty.go` — bounty_search tool handler
- `internal/engine/jobs/bounty_monitor.go:50` — `checkNewBounties()` polling loop
- `internal/engine/jobs/algora_github.go:118` — `ParseGitHubIssueURL()`

**Build:** `cd /home/krolik/src/go-job && go build -buildvcs=false ./...`

**File size rule:** ≤200 lines per source file.

---

### Task 1: Create Opire scraper

**Files:**
- Create: `internal/engine/jobs/opire.go`

**Step 1: Create opire.go**

Implement `SearchOpire(ctx, limit) ([]engine.BountyListing, error)`:
1. Cache check via `engine.CacheLoadJSON` with key `"opire_scrape"`
2. HTTP GET `https://app.opire.dev/home` with RSC headers
3. Find `"initialRewards":[` in response body
4. Extract JSON array by tracking bracket depth
5. Parse into `[]opireReward` struct
6. Map to `[]engine.BountyListing` (source="opire", amount formatted from cents)
7. Cache with `engine.CacheStoreJSON`

Helper: `formatCentsUSD(cents int) string` — converts 150000 → "$1,500"

**Step 2: Build and verify**
```bash
cd /home/krolik/src/go-job && go build -buildvcs=false ./...
```

**Step 3: Commit**
```bash
git add internal/engine/jobs/opire.go
git commit -m "feat: add Opire scraper — RSC endpoint parser"
```

---

### Task 2: Integrate Opire into bounty_search

**Files:**
- Modify: `internal/jobserver/tool_bounty.go`

**Step 1: Add Opire fetch after Algora**

After `SearchAlgoraEnriched()` call, add:
```go
opireBounties, opireErr := jobs.SearchOpire(ctx, 30)
if opireErr != nil {
    slog.Warn("bounty_search: opire error", slog.Any("error", opireErr))
}
for _, ob := range opireBounties {
    bvecs = append(bvecs, jobs.BountyWithVector{Bounty: ob})
}
```

Update error handling to check both sources. Update description to mention Opire.

**Step 2: Build and verify**
**Step 3: Commit**
```bash
git commit -m "feat: integrate Opire into bounty_search tool"
```

---

### Task 3: Integrate Opire into bounty_monitor

**Files:**
- Modify: `internal/engine/jobs/bounty_monitor.go`

**Step 1: Merge Opire bounties in checkNewBounties()**

After `searchAlgoraAPI()` call, add Opire fetch and append to bounties slice. Both sources feed into the same seen-set and notification logic.

**Step 2: Build and verify**
**Step 3: Commit**
```bash
git commit -m "feat: add Opire to bounty_monitor polling"
```

---

### Task 4: Deploy and test

**Step 1: Sync vendor**
```bash
cd /home/krolik/src/go-job && GOWORK=off go mod vendor
```

**Step 2: Build and deploy**
```bash
cd /home/krolik/deploy/krolik-server && docker compose build --no-cache go-job && docker compose up -d --no-deps --force-recreate go-job
```

**Step 3: Test**
- Call `bounty_search` — verify results include `source: "opire"` alongside `source: "algora"`
- Check logs for `bounty_monitor: initialized seen set` with count > 9 (was 9 Algora-only)

**Step 4: Commit**
```bash
git commit -m "chore: sync vendor for Opire integration"
```
