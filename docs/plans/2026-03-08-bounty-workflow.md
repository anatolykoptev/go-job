# Bounty Workflow Tools Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add bounty_attempt, bounty_monitor, and bounty_analyze tools for the full bounty lifecycle.

**Architecture:** Three new features in go-job. `bounty_attempt` comments on GitHub issues via API. `bounty_monitor` polls Algora tRPC in a background goroutine and sends Telegram alerts via vaelor. `bounty_analyze` fetches issue body and uses LLM to estimate complexity. All tools registered in `register.go`.

**Tech Stack:** Go, GitHub REST API, Algora tRPC API, vaelor HTTP API (message tool), LLM via `engine.CallLLM`, Redis cache

---

## Context for the implementer

**Project:** `/home/krolik/src/go-job` — Go MCP server for job/bounty search.

**Key existing code:**
- `internal/engine/jobs/algora_github.go:118` — `ParseGitHubIssueURL(url) (owner, repo, number, ok)` parses GitHub issue URLs
- `internal/engine/jobs/algora_api.go` — `searchAlgoraAPI(ctx, limit)` fetches from Algora tRPC API at `https://console.algora.io/api/trpc/bounty.list`
- `internal/engine/config.go` — `engine.Cfg` holds all config including `GithubToken`, `HTTPClient`
- `internal/engine/bridge_llm.go:15` — `engine.CallLLM(ctx, prompt)` sends prompt to LLM
- `internal/engine/cache.go:100-115` — `engine.CacheLoadJSON[T]()` / `engine.CacheStoreJSON[T]()` for Redis cache
- `internal/engine/types_jobs.go:257-276` — `BountySearchInput`, `BountyListing`, `BountySearchOutput` types
- `internal/jobserver/register.go` — `RegisterTools(server)` registers all MCP tools
- `internal/jobserver/tool_bounty.go` — existing `bounty_search` tool pattern to follow
- `main.go` — engine init, env var wiring, goroutine startup

**Tool registration pattern** (from `tool_bounty.go`):
```go
func registerBountySearch(server *mcp.Server) {
    mcp.AddTool(server, &mcp.Tool{
        Name:        "bounty_search",
        Description: "...",
        Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
    }, func(ctx context.Context, req *mcp.CallToolRequest, input engine.BountySearchInput) (*mcp.CallToolResult, engine.SmartSearchOutput, error) {
        // ...
        return bountyResult(out)
    })
}
```

**Build & deploy:**
```bash
cd /home/krolik/src/go-job && go build -buildvcs=false ./...
cd /home/krolik/deploy/krolik-server && docker compose build --no-cache go-job && docker compose up -d --no-deps --force-recreate go-job
```

**Vaelor message API** (confirmed working):
```bash
curl -X POST http://127.0.0.1:18790/api/tools/message \
  -H 'Content-Type: application/json' \
  -d '{"content":"text","channel":"telegram","chat_id":"428660"}'
# Response: {"duration_ms":0,"result":"Message sent to telegram:428660","tool":"message"}
```

**File size rule:** All source files must be ≤200 lines per `/home/krolik/CLAUDE.md`.

---

### Task 1: `bounty_attempt` — Comment `/attempt` on GitHub issue

**Files:**
- Create: `internal/engine/jobs/algora_attempt.go`
- Create: `internal/jobserver/tool_bounty_attempt.go`
- Modify: `internal/jobserver/register.go`
- Modify: `internal/engine/types_jobs.go`

**Step 1: Add input/output types**

Add to `internal/engine/types_jobs.go` after `BountySearchOutput`:

```go
// BountyAttemptInput is the input for the bounty_attempt tool.
type BountyAttemptInput struct {
	URL string `json:"url" jsonschema:"GitHub issue URL of the bounty to attempt (e.g. https://github.com/org/repo/issues/123)"`
}

// BountyAttemptOutput is the output for the bounty_attempt tool.
type BountyAttemptOutput struct {
	URL        string `json:"url"`
	CommentURL string `json:"comment_url"`
	Status     string `json:"status"`
}
```

**Step 2: Implement GitHub comment function**

Create `internal/engine/jobs/algora_attempt.go`:

```go
package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// githubCommentResponse is the response from GitHub's create comment API.
type githubCommentResponse struct {
	HTMLURL string `json:"html_url"`
}

// CommentOnIssue posts a comment on a GitHub issue.
// Returns the HTML URL of the created comment.
func CommentOnIssue(ctx context.Context, owner, repo string, number int, body string) (string, error) {
	if engine.Cfg.GithubToken == "" {
		return "", fmt.Errorf("GITHUB_TOKEN not configured")
	}

	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/issues/%d/comments", owner, repo, number)
	payload, _ := json.Marshal(map[string]string{"body": body})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("Authorization", "Bearer "+engine.Cfg.GithubToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := engine.Cfg.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("github comment request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("github comment failed (status %d): %s", resp.StatusCode, respBody)
	}

	var result githubCommentResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("github comment response parse failed: %w", err)
	}
	return result.HTMLURL, nil
}
```

**Step 3: Create MCP tool**

Create `internal/jobserver/tool_bounty_attempt.go`:

```go
package jobserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerBountyAttempt(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "bounty_attempt",
		Description: "Claim a bounty by commenting /attempt on a GitHub issue. This signals to the maintainer that you are starting work on the bounty.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input engine.BountyAttemptInput) (*mcp.CallToolResult, engine.SmartSearchOutput, error) {
		owner, repo, number, ok := jobs.ParseGitHubIssueURL(input.URL)
		if !ok {
			return nil, engine.SmartSearchOutput{}, fmt.Errorf("invalid GitHub issue URL: %s", input.URL)
		}

		comment := fmt.Sprintf("/attempt #%d", number)
		commentURL, err := jobs.CommentOnIssue(ctx, owner, repo, number, comment)
		if err != nil {
			return nil, engine.SmartSearchOutput{}, fmt.Errorf("failed to comment: %w", err)
		}

		out := engine.BountyAttemptOutput{
			URL:        input.URL,
			CommentURL: commentURL,
			Status:     fmt.Sprintf("Successfully claimed bounty. Comment: %s", commentURL),
		}

		jsonBytes, _ := json.Marshal(out)
		return nil, engine.SmartSearchOutput{
			Query:   input.URL,
			Answer:  string(jsonBytes),
			Sources: []engine.SourceItem{},
		}, nil
	})
}
```

**Step 4: Register tool**

Add to `internal/jobserver/register.go` after `registerBountySearch(server)`:

```go
	registerBountyAttempt(server)
```

**Step 5: Build and verify**

```bash
cd /home/krolik/src/go-job && go build -buildvcs=false ./...
```

**Step 6: Commit**

```bash
cd /home/krolik/src/go-job && git add internal/engine/jobs/algora_attempt.go internal/jobserver/tool_bounty_attempt.go internal/engine/types_jobs.go internal/jobserver/register.go
git commit -m "feat: add bounty_attempt tool — claim bounties via /attempt comment"
```

---

### Task 2: `bounty_analyze` — Complexity analysis via LLM

**Files:**
- Create: `internal/engine/jobs/algora_analyze.go`
- Create: `internal/jobserver/tool_bounty_analyze.go`
- Modify: `internal/engine/types_jobs.go`
- Modify: `internal/jobserver/register.go`

**Step 1: Add input/output types**

Add to `internal/engine/types_jobs.go`:

```go
// BountyAnalyzeInput is the input for the bounty_analyze tool.
type BountyAnalyzeInput struct {
	URL string `json:"url" jsonschema:"GitHub issue URL of the bounty to analyze (e.g. https://github.com/org/repo/issues/123)"`
}

// BountyAnalysis is the LLM-generated complexity analysis.
type BountyAnalysis struct {
	Title       string   `json:"title"`
	Amount      string   `json:"amount"`
	Complexity  int      `json:"complexity"`  // 1-5
	EstHours    string   `json:"est_hours"`   // e.g. "4-8 hours"
	DollarPerHr string   `json:"dollar_per_hr"` // e.g. "$62-125/hr"
	Skills      []string `json:"skills_needed"`
	Summary     string   `json:"summary"`
	Verdict     string   `json:"verdict"` // "recommended", "fair", "avoid"
}
```

**Step 2: Implement analysis function**

Create `internal/engine/jobs/algora_analyze.go`:

```go
package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/anatolykoptev/go_job/internal/engine"
)

const bountyAnalyzePrompt = `Analyze this GitHub bounty issue and estimate the effort required.

Issue title: %s
Bounty amount: %s
Repository: %s/%s
Issue body:
%s

Respond with ONLY valid JSON (no markdown, no backticks):
{
  "complexity": <1-5 integer, 1=trivial fix, 5=major feature>,
  "est_hours": "<range like '2-4 hours' or '1-2 days'>",
  "dollar_per_hr": "<estimated $/hr based on amount and hours>",
  "skills_needed": ["skill1", "skill2"],
  "summary": "<2-3 sentence assessment of the task>",
  "verdict": "<one of: recommended, fair, avoid>"
}

Rules:
- "recommended" = good pay for effort, clear requirements
- "fair" = reasonable but not great
- "avoid" = underpaid, vague requirements, or very complex`

// AnalyzeBounty fetches an issue body and uses LLM to estimate complexity.
func AnalyzeBounty(ctx context.Context, issueURL string) (*engine.BountyAnalysis, error) {
	owner, repo, number, ok := ParseGitHubIssueURL(issueURL)
	if !ok {
		return nil, fmt.Errorf("invalid GitHub issue URL: %s", issueURL)
	}

	// Try to find bounty in cached enriched data first.
	title, amount, body := "", "", ""
	if bvecs, err := SearchAlgoraEnriched(ctx, 50); err == nil {
		for _, bv := range bvecs {
			if bv.Bounty.URL == issueURL {
				title = bv.Bounty.Title
				amount = bv.Bounty.Amount
				break
			}
		}
	}

	// Fetch issue body from GitHub API.
	body, err := FetchGitHubIssueBody(ctx, owner, repo, number)
	if err != nil {
		slog.Warn("bounty_analyze: failed to fetch issue body", slog.Any("error", err))
		body = "(issue body unavailable)"
	}

	if title == "" {
		title = fmt.Sprintf("%s/%s#%d", owner, repo, number)
	}

	prompt := fmt.Sprintf(bountyAnalyzePrompt, title, amount, owner, repo, body)
	raw, err := engine.CallLLM(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("LLM analysis failed: %w", err)
	}

	var analysis engine.BountyAnalysis
	if err := json.Unmarshal([]byte(raw), &analysis); err != nil {
		return nil, fmt.Errorf("LLM response parse failed: %w (raw: %s)", err, raw[:min(len(raw), 200)])
	}

	analysis.Title = title
	analysis.Amount = amount
	return &analysis, nil
}

// FetchGitHubIssueBody fetches the body text of a GitHub issue.
func FetchGitHubIssueBody(ctx context.Context, owner, repo string, number int) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, engine.Cfg.FetchTimeout)
	defer cancel()

	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/issues/%d", owner, repo, number)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", engine.UserAgentBot)
	if engine.Cfg.GithubToken != "" {
		req.Header.Set("Authorization", "Bearer "+engine.Cfg.GithubToken)
	}

	resp, err := engine.Cfg.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github issue API returned %d", resp.StatusCode)
	}

	var issue struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&issue); err != nil {
		return "", err
	}
	return issue.Body, nil
}
```

**Important:** This file needs `"net/http"` in the imports. Add it to the import block.

**Step 3: Create MCP tool**

Create `internal/jobserver/tool_bounty_analyze.go`:

```go
package jobserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerBountyAnalyze(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "bounty_analyze",
		Description: "Analyze a bounty's complexity, estimate hours, $/hr rate, and whether it's worth taking. Fetches the GitHub issue body and uses AI to assess effort vs reward.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input engine.BountyAnalyzeInput) (*mcp.CallToolResult, engine.SmartSearchOutput, error) {
		analysis, err := jobs.AnalyzeBounty(ctx, input.URL)
		if err != nil {
			return nil, engine.SmartSearchOutput{}, fmt.Errorf("analysis failed: %w", err)
		}

		jsonBytes, _ := json.Marshal(analysis)
		return nil, engine.SmartSearchOutput{
			Query:   input.URL,
			Answer:  string(jsonBytes),
			Sources: []engine.SourceItem{},
		}, nil
	})
}
```

**Step 4: Register tool**

Add to `internal/jobserver/register.go` after `registerBountyAttempt(server)`:

```go
	registerBountyAnalyze(server)
```

**Step 5: Build and verify**

```bash
cd /home/krolik/src/go-job && go build -buildvcs=false ./...
```

**Step 6: Commit**

```bash
cd /home/krolik/src/go-job && git add internal/engine/jobs/algora_analyze.go internal/jobserver/tool_bounty_analyze.go internal/engine/types_jobs.go internal/jobserver/register.go
git commit -m "feat: add bounty_analyze tool — LLM complexity estimation"
```

---

### Task 3: `bounty_monitor` — Background new bounty alerts

**Files:**
- Create: `internal/engine/jobs/bounty_monitor.go`
- Create: `internal/engine/jobs/vaelor_notify.go`
- Modify: `internal/engine/config.go`
- Modify: `main.go`

**Step 1: Add config fields**

Add to `internal/engine/config.go` `Config` struct, after the bounty tuning section:

```go
	// Bounty monitor.
	VaelorNotifyURL    string        // VAELOR_NOTIFY_URL for sending Telegram notifications
	BountyNotifyChatID string        // BOUNTY_NOTIFY_CHAT_ID (default "428660")
	BountyMonitorInterval time.Duration // BOUNTY_MONITOR_INTERVAL (default 15m)
```

**Step 2: Create vaelor notification client**

Create `internal/engine/jobs/vaelor_notify.go`:

```go
package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// vaelorToolRequest is the request body for vaelor's tool execution API.
type vaelorToolRequest struct {
	Content string `json:"content"`
	Channel string `json:"channel"`
	ChatID  string `json:"chat_id"`
}

// SendTelegramNotification sends a message via vaelor's message tool.
func SendTelegramNotification(ctx context.Context, message string) error {
	baseURL := engine.Cfg.VaelorNotifyURL
	if baseURL == "" {
		return fmt.Errorf("VAELOR_NOTIFY_URL not configured")
	}
	chatID := engine.Cfg.BountyNotifyChatID
	if chatID == "" {
		chatID = "428660"
	}

	payload, _ := json.Marshal(vaelorToolRequest{
		Content: message,
		Channel: "telegram",
		ChatID:  chatID,
	})

	url := baseURL + "/api/tools/message"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := engine.Cfg.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("vaelor notify failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("vaelor notify status %d: %s", resp.StatusCode, body)
	}
	return nil
}
```

**Step 3: Create monitor goroutine**

Create `internal/engine/jobs/bounty_monitor.go`:

```go
package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/anatolykoptev/go_job/internal/engine"
)

const bountySeenIDsKey = "bounty_seen_ids"

// StartBountyMonitor launches a background goroutine that polls for new bounties
// and sends Telegram notifications via vaelor.
func StartBountyMonitor(ctx context.Context) {
	interval := engine.Cfg.BountyMonitorInterval
	if interval <= 0 {
		interval = 15 * time.Minute
	}

	if engine.Cfg.VaelorNotifyURL == "" {
		slog.Info("bounty_monitor: disabled (VAELOR_NOTIFY_URL not set)")
		return
	}

	slog.Info("bounty_monitor: starting", slog.Duration("interval", interval))

	// Initial run after short delay (let caches warm up).
	time.AfterFunc(30*time.Second, func() {
		checkNewBounties(ctx)
	})

	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				slog.Info("bounty_monitor: stopped")
				return
			case <-ticker.C:
				checkNewBounties(ctx)
			}
		}
	}()
}

func checkNewBounties(ctx context.Context) {
	bounties, err := searchAlgoraAPI(ctx, 50)
	if err != nil {
		slog.Warn("bounty_monitor: fetch failed", slog.Any("error", err))
		return
	}

	// Load previously seen IDs from cache.
	seenIDs, _ := engine.CacheLoadJSON[map[string]bool](ctx, bountySeenIDsKey)
	if seenIDs == nil {
		// First run — store all current IDs without notifying.
		seenIDs = make(map[string]bool, len(bounties))
		for _, b := range bounties {
			seenIDs[b.URL] = true
		}
		engine.CacheStoreJSON(ctx, bountySeenIDsKey, "", seenIDs)
		slog.Info("bounty_monitor: initialized seen set", slog.Int("count", len(seenIDs)))
		return
	}

	// Find new bounties.
	var newBounties []engine.BountyListing
	for _, b := range bounties {
		if !seenIDs[b.URL] {
			newBounties = append(newBounties, b)
			seenIDs[b.URL] = true
		}
	}

	if len(newBounties) == 0 {
		return
	}

	// Update seen set.
	engine.CacheStoreJSON(ctx, bountySeenIDsKey, "", seenIDs)

	// Send notification for each new bounty.
	for _, b := range newBounties {
		msg := formatBountyNotification(b)
		if err := SendTelegramNotification(ctx, msg); err != nil {
			slog.Warn("bounty_monitor: notify failed", slog.Any("error", err), slog.String("url", b.URL))
		} else {
			slog.Info("bounty_monitor: notified", slog.String("url", b.URL))
		}
	}
}

func formatBountyNotification(b engine.BountyListing) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("New Bounty %s\n", b.Amount))
	sb.WriteString(fmt.Sprintf("%s\n", b.Title))
	if len(b.Skills) > 0 {
		sb.WriteString(fmt.Sprintf("Skills: %s\n", strings.Join(b.Skills, ", ")))
	}
	sb.WriteString(b.URL)
	return sb.String()
}
```

**Step 4: Wire env vars in main.go**

Add to `initEngine()` config struct in `main.go`, after `BountyMinRelevance`:

```go
		VaelorNotifyURL:       env.Str("VAELOR_NOTIFY_URL", ""),
		BountyNotifyChatID:    env.Str("BOUNTY_NOTIFY_CHAT_ID", "428660"),
		BountyMonitorInterval: env.Duration("BOUNTY_MONITOR_INTERVAL", 15*time.Minute),
```

Add at the end of `initEngine()`, before the closing `}`:

```go
	// Start bounty monitor (background goroutine).
	jobs.StartBountyMonitor(context.Background())
```

**Step 5: Add env var to docker compose**

Add to `compose/apps.yml` go-job environment section:

```yaml
      - VAELOR_NOTIFY_URL=http://host.docker.internal:18790
```

Note: vaelor runs on the host (not in docker), so we use `host.docker.internal` or the host IP. Check if the docker network allows this — if not, use the host's bridge IP (typically `172.17.0.1`).

**Step 6: Build and verify**

```bash
cd /home/krolik/src/go-job && go build -buildvcs=false ./...
```

**Step 7: Commit**

```bash
cd /home/krolik/src/go-job && git add internal/engine/jobs/bounty_monitor.go internal/engine/jobs/vaelor_notify.go internal/engine/config.go main.go
git commit -m "feat: add bounty_monitor — background Telegram alerts for new bounties"
```

---

### Task 4: Deploy and test all features

**Step 1: Update docker compose env**

Add `VAELOR_NOTIFY_URL` to `/home/krolik/deploy/krolik-server/compose/apps.yml`.

**Step 2: Build and deploy**

```bash
cd /home/krolik/deploy/krolik-server && docker compose build --no-cache go-job && docker compose up -d --no-deps --force-recreate go-job
```

**Step 3: Test bounty_attempt**

Call `bounty_attempt` with a test issue URL. Verify the `/attempt` comment appears on GitHub.

**Step 4: Test bounty_analyze**

Call `bounty_analyze` with a bounty URL. Verify LLM returns structured analysis with complexity, hours, $/hr.

**Step 5: Test bounty_monitor**

Check `docker logs go-job` for `bounty_monitor: starting` and `bounty_monitor: initialized seen set`. Verify Telegram notification arrives when a new bounty appears (or manually test `SendTelegramNotification`).

**Step 6: Update tool count in main.go**

Update `slog.Info("tools registered", slog.Int("count", 25))` to `27` (25 + bounty_attempt + bounty_analyze).

**Step 7: Commit and tag release**

```bash
cd /home/krolik/src/go-job
git add -A
git commit -m "feat: bounty workflow tools v1.4.0 — attempt, analyze, monitor"
git tag v1.4.0
git push origin main v1.4.0
```
