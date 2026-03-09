# BountyHub + Boss.dev Integration Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add BountyHub and Boss.dev as bounty sources to go-job, tripling the pool of available bounties.

**Architecture:** Each source is a standalone scraper file in `internal/engine/jobs/` returning `[]engine.BountyListing`. Both have clean JSON APIs requiring no auth. Scrapers integrate into existing `tool_bounty.go` merge logic and `bounty_monitor.go` polling.

**Tech Stack:** Go, net/http, encoding/json. No new dependencies.

---

### Task 1: BountyHub scraper

**Files:**
- Create: `internal/engine/jobs/bountyhub.go`
- Test: `internal/engine/jobs/bountyhub_test.go`

**Step 1: Write the test file**

```go
package jobs

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sampleBountyHubResponse = `{
  "data": [
    {
      "id": "abc-123",
      "title": "Fix memory leak in parser",
      "htmlURL": "https://github.com/org/repo/issues/42",
      "language": "Go",
      "repositoryFullName": "org/repo",
      "issueNumber": 42,
      "issueState": "open",
      "totalAmount": "500.00",
      "solved": false,
      "claimed": false,
      "createdAt": "2026-03-01T12:00:00Z"
    },
    {
      "id": "def-456",
      "title": "Add CSV export",
      "htmlURL": "https://github.com/other/lib/issues/7",
      "language": "Python",
      "repositoryFullName": "other/lib",
      "issueNumber": 7,
      "issueState": "open",
      "totalAmount": "120.50",
      "solved": false,
      "claimed": false,
      "createdAt": "2026-02-15T08:30:00Z"
    }
  ],
  "hasNextPage": false
}`

func TestParseBountyHubResponse(t *testing.T) {
	t.Parallel()
	bounties, hasNext, err := parseBountyHubResponse([]byte(sampleBountyHubResponse))
	require.NoError(t, err)
	assert.False(t, hasNext)
	require.Len(t, bounties, 2)

	b := bounties[0]
	assert.Equal(t, "Fix memory leak in parser", b.Title)
	assert.Equal(t, "org/repo", b.Org)
	assert.Equal(t, "https://github.com/org/repo/issues/42", b.URL)
	assert.Equal(t, "$500", b.Amount)
	assert.Equal(t, "USD", b.Currency)
	assert.Equal(t, []string{"Go"}, b.Skills)
	assert.Equal(t, "bountyhub", b.Source)
	assert.Equal(t, "#42", b.IssueNum)
	assert.NotEmpty(t, b.Posted)

	b2 := bounties[1]
	assert.Equal(t, "$120", b2.Amount)
	assert.Equal(t, []string{"Python"}, b2.Skills)
}

func TestParseBountyHubResponse_empty(t *testing.T) {
	t.Parallel()
	bounties, _, err := parseBountyHubResponse([]byte(`{"data":[],"hasNextPage":false}`))
	require.NoError(t, err)
	assert.Empty(t, bounties)
}
```

**Step 2: Run test to verify it fails**

Run: `cd ~/src/go-job && go test ./internal/engine/jobs/ -run TestParseBountyHub -v`
Expected: FAIL — `parseBountyHubResponse` undefined

**Step 3: Write the scraper**

```go
package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/anatolykoptev/go_job/internal/engine"
)

const (
	bountyHubAPIURL       = "https://api.bountyhub.dev/api/bounties"
	bountyHubScrapeCacheKey = "bountyhub_scrape"
)

type bountyHubResponse struct {
	Data        []bountyHubItem `json:"data"`
	HasNextPage bool            `json:"hasNextPage"`
}

type bountyHubItem struct {
	ID                 string  `json:"id"`
	Title              string  `json:"title"`
	HtmlURL            string  `json:"htmlURL"`
	Language           string  `json:"language"`
	RepositoryFullName string  `json:"repositoryFullName"`
	IssueNumber        int     `json:"issueNumber"`
	IssueState         string  `json:"issueState"`
	TotalAmount        string  `json:"totalAmount"`
	Solved             bool    `json:"solved"`
	Claimed            bool    `json:"claimed"`
	CreatedAt          string  `json:"createdAt"`
}

// SearchBountyHub fetches open bounties from BountyHub. Results are cached.
func SearchBountyHub(ctx context.Context, limit int) ([]engine.BountyListing, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	if cached, ok := engine.CacheLoadJSON[[]engine.BountyListing](ctx, bountyHubScrapeCacheKey); ok {
		slog.Debug("bountyhub: using cached results", slog.Int("results", len(cached)))
		if len(cached) > limit {
			cached = cached[:limit]
		}
		return cached, nil
	}

	bounties, err := fetchBountyHub(ctx)
	if err != nil {
		return nil, err
	}

	engine.CacheStoreJSON(ctx, bountyHubScrapeCacheKey, "", bounties)
	if len(bounties) > limit {
		bounties = bounties[:limit]
	}

	slog.Debug("bountyhub: fetch complete", slog.Int("results", len(bounties)))
	return bounties, nil
}

func fetchBountyHub(ctx context.Context) ([]engine.BountyListing, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, engine.Cfg.FetchTimeout)
	defer cancel()

	url := bountyHubAPIURL + `?page=1&limit=50&filters={"solved":false}&sort=[{"orderBy":"totalAmount","order":"desc"}]`

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", engine.UserAgentChrome)

	resp, err := engine.Cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bountyhub request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bountyhub returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return nil, err
	}

	bounties, _, err := parseBountyHubResponse(body)
	return bounties, err
}

func parseBountyHubResponse(data []byte) ([]engine.BountyListing, bool, error) {
	var resp bountyHubResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, false, fmt.Errorf("bountyhub: JSON parse failed: %w", err)
	}

	bounties := make([]engine.BountyListing, 0, len(resp.Data))
	for _, item := range resp.Data {
		if item.Solved || item.HtmlURL == "" {
			continue
		}

		amount := formatBountyHubAmount(item.TotalAmount)

		var skills []string
		if item.Language != "" {
			skills = []string{item.Language}
		}

		bounties = append(bounties, engine.BountyListing{
			Title:    item.Title,
			Org:      item.RepositoryFullName,
			URL:      item.HtmlURL,
			Amount:   amount,
			Currency: "USD",
			Skills:   skills,
			Source:   "bountyhub",
			IssueNum: "#" + strconv.Itoa(item.IssueNumber),
			Posted:   item.CreatedAt,
		})
	}

	return bounties, resp.HasNextPage, nil
}

// formatBountyHubAmount converts "500.00" to "$500", "1234.56" to "$1,234".
func formatBountyHubAmount(s string) string {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f <= 0 {
		return "$0"
	}
	dollars := int(math.Round(f))
	return formatCentsUSD(dollars * 100)
}
```

**Step 4: Run tests**

Run: `cd ~/src/go-job && go test ./internal/engine/jobs/ -run TestParseBountyHub -v`
Expected: PASS

**Step 5: Commit**

```bash
cd ~/src/go-job
git add internal/engine/jobs/bountyhub.go internal/engine/jobs/bountyhub_test.go
git commit -m "feat(bounty): add BountyHub scraper

Public JSON API at api.bountyhub.dev, no auth required.
0% commission for developers."
```

---

### Task 2: Boss.dev scraper

**Files:**
- Create: `internal/engine/jobs/boss.go`
- Test: `internal/engine/jobs/boss_test.go`

**Step 1: Write the test file**

```go
package jobs

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sampleBossResponse = `[
  {
    "gid": "github/MDU6SXNzdWU0NjM4NTI2MzM=",
    "hId": "kistek/boss-demo#3",
    "sByC": {"EUR": 1200, "USD": 1234},
    "status": "open",
    "title": "Demo GitHub Issue with Bounty",
    "url": "https://github.com/kistek/boss-demo/issues/3",
    "usd": 2434
  },
  {
    "gid": "github/other123",
    "hId": "org/repo#15",
    "sByC": {"USD": 500},
    "status": "open",
    "title": "Fix login bug",
    "url": "https://github.com/org/repo/issues/15",
    "usd": 500
  }
]`

func TestParseBossResponse(t *testing.T) {
	t.Parallel()
	bounties, err := parseBossResponse([]byte(sampleBossResponse))
	require.NoError(t, err)
	require.Len(t, bounties, 2)

	b := bounties[0]
	assert.Equal(t, "Demo GitHub Issue with Bounty", b.Title)
	assert.Equal(t, "kistek/boss-demo", b.Org)
	assert.Equal(t, "https://github.com/kistek/boss-demo/issues/3", b.URL)
	assert.Equal(t, "$2,434", b.Amount)
	assert.Equal(t, "USD", b.Currency)
	assert.Equal(t, "boss", b.Source)
	assert.Equal(t, "#3", b.IssueNum)

	b2 := bounties[1]
	assert.Equal(t, "$500", b2.Amount)
	assert.Equal(t, "#15", b2.IssueNum)
}

func TestParseBossResponse_empty(t *testing.T) {
	t.Parallel()
	bounties, err := parseBossResponse([]byte(`[]`))
	require.NoError(t, err)
	assert.Empty(t, bounties)
}
```

**Step 2: Run test to verify it fails**

Run: `cd ~/src/go-job && go test ./internal/engine/jobs/ -run TestParseBoss -v`
Expected: FAIL — `parseBossResponse` undefined

**Step 3: Write the scraper**

```go
package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/anatolykoptev/go_job/internal/engine"
)

const (
	bossAPIURL       = "https://api.boss.dev/rpc/issues/gh/unsolved"
	bossScrapeCacheKey = "boss_scrape"
)

type bossIssue struct {
	GID    string         `json:"gid"`
	HID    string         `json:"hId"`
	SByC   map[string]int `json:"sByC"`
	Status string         `json:"status"`
	Title  string         `json:"title"`
	URL    string         `json:"url"`
	USD    int            `json:"usd"`
}

// SearchBoss fetches open bounties from Boss.dev. Results are cached.
func SearchBoss(ctx context.Context, limit int) ([]engine.BountyListing, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	if cached, ok := engine.CacheLoadJSON[[]engine.BountyListing](ctx, bossScrapeCacheKey); ok {
		slog.Debug("boss: using cached results", slog.Int("results", len(cached)))
		if len(cached) > limit {
			cached = cached[:limit]
		}
		return cached, nil
	}

	bounties, err := fetchBoss(ctx)
	if err != nil {
		return nil, err
	}

	engine.CacheStoreJSON(ctx, bossScrapeCacheKey, "", bounties)
	if len(bounties) > limit {
		bounties = bounties[:limit]
	}

	slog.Debug("boss: fetch complete", slog.Int("results", len(bounties)))
	return bounties, nil
}

func fetchBoss(ctx context.Context) ([]engine.BountyListing, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, engine.Cfg.FetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, bossAPIURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", engine.UserAgentChrome)

	resp, err := engine.Cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("boss request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("boss returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return nil, err
	}

	return parseBossResponse(body)
}

func parseBossResponse(data []byte) ([]engine.BountyListing, error) {
	var issues []bossIssue
	if err := json.Unmarshal(data, &issues); err != nil {
		return nil, fmt.Errorf("boss: JSON parse failed: %w", err)
	}

	bounties := make([]engine.BountyListing, 0, len(issues))
	for _, issue := range issues {
		if issue.URL == "" || issue.Status != "open" {
			continue
		}

		amount := formatCentsUSD(issue.USD * 100)
		org, issueNum := parseBossHID(issue.HID)

		bounties = append(bounties, engine.BountyListing{
			Title:    issue.Title,
			Org:      org,
			URL:      issue.URL,
			Amount:   amount,
			Currency: "USD",
			Source:   "boss",
			IssueNum: issueNum,
		})
	}

	return bounties, nil
}

// parseBossHID extracts org and issue number from "owner/repo#123".
func parseBossHID(hid string) (org, issueNum string) {
	parts := strings.SplitN(hid, "#", 2)
	if len(parts) == 2 {
		org = parts[0]
		issueNum = "#" + parts[1]
	} else {
		org = hid
	}
	return org, issueNum
}
```

**Step 4: Run tests**

Run: `cd ~/src/go-job && go test ./internal/engine/jobs/ -run TestParseBoss -v`
Expected: PASS

**Step 5: Commit**

```bash
cd ~/src/go-job
git add internal/engine/jobs/boss.go internal/engine/jobs/boss_test.go
git commit -m "feat(bounty): add Boss.dev scraper

Public JSON API at api.boss.dev, no auth required.
Auto-pays when GitHub issue is closed."
```

---

### Task 3: Integrate into bounty_search tool

**Files:**
- Modify: `internal/jobserver/tool_bounty.go:22-34`

**Step 1: Add BountyHub and Boss.dev fetches after Opire merge**

In `registerBountySearch()`, after the Opire merge block (line ~34), add:

```go
// Also fetch BountyHub bounties and merge.
bhBounties, bhErr := jobs.SearchBountyHub(ctx, 50)
if bhErr != nil {
    slog.Warn("bounty_search: bountyhub error", slog.Any("error", bhErr))
}
for _, b := range bhBounties {
    bvecs = append(bvecs, jobs.BountyWithVector{Bounty: b})
}

// Also fetch Boss.dev bounties and merge.
bossBounties, bossErr := jobs.SearchBoss(ctx, 50)
if bossErr != nil {
    slog.Warn("bounty_search: boss error", slog.Any("error", bossErr))
}
for _, b := range bossBounties {
    bvecs = append(bvecs, jobs.BountyWithVector{Bounty: b})
}
```

**Step 2: Update tool description**

Change line 18 description from:
```
"Search for open-source bounties on Algora.io and Opire.dev..."
```
to:
```
"Search for open-source bounties on Algora.io, Opire.dev, BountyHub.dev, and Boss.dev. Returns paid GitHub issues with bounty amounts. Filter by technology, keyword, minimum amount, or required skills."
```

**Step 3: Update error check (line 37-39)**

Replace the dual-error check with a check that all sources failed:

```go
if len(bvecs) == 0 {
    if err != nil && opireErr != nil && bhErr != nil && bossErr != nil {
        return nil, engine.SmartSearchOutput{}, fmt.Errorf("bounty fetch failed: algora: %v; opire: %v; bountyhub: %v; boss: %v", err, opireErr, bhErr, bossErr)
    }
    return bountyResult(engine.BountySearchOutput{Query: input.Query, Summary: "No bounties found."})
}
```

**Step 4: Run existing tests**

Run: `cd ~/src/go-job && go build ./...`
Expected: Build succeeds

**Step 5: Commit**

```bash
cd ~/src/go-job
git add internal/jobserver/tool_bounty.go
git commit -m "feat(bounty): integrate BountyHub and Boss.dev into bounty_search"
```

---

### Task 4: Integrate into bounty_monitor

**Files:**
- Modify: `internal/engine/jobs/bounty_monitor.go:50-68`

**Step 1: Add BountyHub and Boss.dev fetches in `checkNewBounties()`**

After the Opire merge block (line ~61), add:

```go
// Also fetch BountyHub bounties and merge.
bhBounties, bhErr := SearchBountyHub(ctx, 50)
if bhErr != nil {
    slog.Warn("bounty_monitor: bountyhub fetch failed", slog.Any("error", bhErr))
}
bounties = append(bounties, bhBounties...)

// Also fetch Boss.dev bounties and merge.
bossBounties, bossErr := SearchBoss(ctx, 50)
if bossErr != nil {
    slog.Warn("bounty_monitor: boss fetch failed", slog.Any("error", bossErr))
}
bounties = append(bounties, bossBounties...)
```

**Step 2: Update the "all sources failed" check (line 63-65)**

Replace:
```go
if len(bounties) == 0 {
    if err != nil || opireErr != nil {
```
with:
```go
if len(bounties) == 0 {
    if err != nil || opireErr != nil || bhErr != nil || bossErr != nil {
```

**Step 3: Build and verify**

Run: `cd ~/src/go-job && go build ./...`
Expected: Build succeeds

**Step 4: Commit**

```bash
cd ~/src/go-job
git add internal/engine/jobs/bounty_monitor.go
git commit -m "feat(bounty): add BountyHub and Boss.dev to bounty monitor"
```

---

### Task 5: Build, deploy, and verify

**Step 1: Run all tests**

Run: `cd ~/src/go-job && go test ./... -count=1`
Expected: All tests pass

**Step 2: Run linter**

Run: `cd ~/src/go-job && make lint`
Expected: No errors

**Step 3: Build Docker image**

Run: `cd ~/deploy/krolik-server && docker compose build --no-cache go-job`
Expected: Build succeeds

**Step 4: Deploy**

Run: `cd ~/deploy/krolik-server && docker compose up -d --no-deps --force-recreate go-job`
Expected: Container starts

**Step 5: Verify via MCP**

Test `bounty_search` tool — should now return bounties from 4 sources (algora, opire, bountyhub, boss).

**Step 6: Commit tag**

```bash
cd ~/src/go-job && git tag v1.X.0 && git push origin v1.X.0
```
