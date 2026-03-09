# Unified Action-First Opportunity Tools — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Create unified `opportunity_*` MCP tools that aggregate bounties, security bug bounties, and freelance jobs into a single action-based pipeline (search → analyze → claim → submit).

**Architecture:** Action-first design where tools are grouped by action (search/analyze/claim/submit) with a `type` parameter for filtering. URL auto-detection routes to the correct handler. Existing specialized tools remain for backward compatibility.

**Tech Stack:** Go 1.26, go-sdk/mcp, existing engine/jobs infrastructure

---

## Task 1: Create Opportunity unified type

**Files:**
- Create: `internal/engine/types_opportunity.go`

**What to build:**

```go
// Opportunity is the unified type returned by opportunity_search.
// It wraps bounties, security programs, and freelance jobs into one format.
type Opportunity struct {
    Type    string   `json:"type"`    // "bounty", "security", "freelance"
    Title   string   `json:"title"`
    URL     string   `json:"url"`
    Reward  string   `json:"reward"`  // "$500", "Up to $50,000", "$80k-120k/yr"
    Source  string   `json:"source"`  // "algora", "hackerone", "remoteok", etc.
    Skills  []string `json:"skills"`
    Posted  string   `json:"posted,omitempty"`
    Summary string   `json:"summary,omitempty"` // short description or scope
}

// OpportunitySearchInput is the input for opportunity_search.
type OpportunitySearchInput struct {
    Type  string `json:"type,omitempty" jsonschema:"Filter by type: bounty, security, freelance, all (default: all)"`
    Query string `json:"query,omitempty" jsonschema:"Search keywords to filter (e.g. golang, crypto, api). Empty returns all."`
}

// OpportunitySearchOutput is the output for opportunity_search.
type OpportunitySearchOutput struct {
    Query         string        `json:"query"`
    Opportunities []Opportunity `json:"opportunities"`
    Summary       string        `json:"summary"`
}

// OpportunityAnalyzeInput is the input for opportunity_analyze.
type OpportunityAnalyzeInput struct {
    URL string `json:"url" jsonschema:"URL of the opportunity to analyze. Auto-detects type from URL (GitHub issue = bounty, immunefi/hackerone/bugcrowd = security, remoteok/himalayas = freelance)."`
}

// OpportunityAnalysis is the unified analysis output.
type OpportunityAnalysis struct {
    Type        string `json:"type"`        // "bounty", "security", "freelance"
    Title       string `json:"title"`
    URL         string `json:"url"`
    Reward      string `json:"reward"`
    Verdict     string `json:"verdict"`     // "recommended", "fair", "avoid"
    Summary     string `json:"summary"`
    Details     any    `json:"details"`     // type-specific: BountyAnalysis, SecurityAnalysis, etc.
}

// OpportunityClaimInput is the input for opportunity_claim.
type OpportunityClaimInput struct {
    URL string `json:"url" jsonschema:"URL of the opportunity to claim. For bounties: posts /attempt on GitHub issue. For security: no action (manual). For freelance: generates cover letter."`
}
```

**Test:** `go build ./internal/engine/...` — types compile.

**Commit:** `feat(opportunity): add unified Opportunity types`

---

## Task 2: Create opportunity_search aggregator

**Files:**
- Create: `internal/engine/jobs/opportunity_search.go`

**What to build:**

A function `SearchOpportunities(ctx, input OpportunitySearchInput) (OpportunitySearchOutput, error)` that:

1. Based on `input.Type` (or "all"), fetches in parallel:
   - **bounty**: calls existing `SearchAlgoraEnriched`, `SearchOpire`, `SearchBountyHub`, `SearchBoss`, `SearchLightning`, `SearchCollaborators` — converts `BountyListing` → `Opportunity`
   - **security**: calls existing `SearchSecurityPrograms` + `SearchImmunefi` — converts `SecurityProgram` → `Opportunity`
   - **freelance**: calls existing `SearchRemoteOKFreelance` + `SearchHimalayas` — converts `FreelanceJob` → `Opportunity`

2. Merges all into `[]Opportunity`

3. If `input.Query` is set, filters by case-insensitive match on Title, Source, or Skills

4. Caps at 50 results per type, 100 total

5. Returns `OpportunitySearchOutput` with summary

**Conversion helpers** (private functions):
- `bountyToOpportunity(b BountyListing) Opportunity`
- `securityToOpportunity(s engine.SecurityProgram) Opportunity`
- `freelanceToOpportunity(f engine.FreelanceJob) Opportunity`

**Test:** `internal/engine/jobs/opportunity_search_test.go` — test conversion helpers and query filtering.

**Commit:** `feat(opportunity): add opportunity_search aggregator`

---

## Task 3: Create opportunity_search MCP tool

**Files:**
- Create: `internal/jobserver/tool_opportunity_search.go`

**What to build:**

```go
func registerOpportunitySearch(server *mcp.Server) {
    mcp.AddTool(server, &mcp.Tool{
        Name:        "opportunity_search",
        Description: "Search for income opportunities across all sources: code bounties (Algora, Opire, BountyHub, Boss, Lightning, Collaborators), security bug bounties (HackerOne, Bugcrowd, Intigriti, YesWeHack, Immunefi), and freelance jobs (RemoteOK, Himalayas). Filter by type and keyword.",
        Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
    }, func(ctx, req, input OpportunitySearchInput) (*mcp.CallToolResult, SmartSearchOutput, error) {
        // Call jobs.SearchOpportunities(ctx, input)
        // Marshal to JSON, return as SmartSearchOutput
    })
}
```

Follow exact pattern from `tool_security_bounty.go`.

**Commit:** `feat(opportunity): register opportunity_search MCP tool`

---

## Task 4: Create URL auto-detection for opportunity_analyze

**Files:**
- Create: `internal/engine/jobs/opportunity_detect.go`

**What to build:**

```go
// DetectOpportunityType determines the opportunity type from a URL.
// Returns "bounty", "security", "freelance", or "" if unknown.
func DetectOpportunityType(rawURL string) string {
    // github.com/*/issues/* → "bounty"
    // hackerone.com, bugcrowd.com, intigriti.com, yeswehack.com, immunefi.com → "security"
    // remoteok.com, himalayas.app, upwork.com, freelancer.com → "freelance"
    // algora.io → "bounty"
    // opire.dev → "bounty"
    // boss.dev → "bounty"
    // bountyhub.dev → "bounty"
    // "" otherwise
}
```

**Test:** `internal/engine/jobs/opportunity_detect_test.go` — table-driven tests for all URL patterns.

**Commit:** `feat(opportunity): add URL type auto-detection`

---

## Task 5: Create opportunity_analyze MCP tool

**Files:**
- Create: `internal/jobserver/tool_opportunity_analyze.go`

**What to build:**

```go
func registerOpportunityAnalyze(server *mcp.Server) {
    // Input: OpportunityAnalyzeInput{URL}
    // 1. DetectOpportunityType(URL)
    // 2. Switch on type:
    //    - "bounty": call jobs.AnalyzeBounty(ctx, url) → wrap in OpportunityAnalysis
    //    - "security": return basic info (platform, max bounty, targets from search results)
    //    - "freelance": return basic info (title, budget from search results)
    //    - unknown: return error "cannot detect opportunity type from URL"
    // 3. Return as SmartSearchOutput JSON
}
```

**Commit:** `feat(opportunity): register opportunity_analyze MCP tool`

---

## Task 6: Create opportunity_claim MCP tool

**Files:**
- Create: `internal/jobserver/tool_opportunity_claim.go`

**What to build:**

```go
func registerOpportunityClaim(server *mcp.Server) {
    // Input: OpportunityClaimInput{URL}
    // 1. DetectOpportunityType(URL)
    // 2. Switch on type:
    //    - "bounty": parse GitHub issue URL, call jobs.CommentOnIssue(ctx, owner, repo, number, "/attempt")
    //    - "security": return message "Security programs don't have a claim step. Use security_recon to scan targets."
    //    - "freelance": return message "Freelance projects require manual application. Use application_prep for help."
    //    - unknown: error
    // 3. Return result
}
```

**Commit:** `feat(opportunity): register opportunity_claim MCP tool`

---

## Task 7: Register all new tools and update main.go

**Files:**
- Modify: `internal/jobserver/register.go` — add registerOpportunitySearch, registerOpportunityAnalyze, registerOpportunityClaim
- Modify: `main.go` — update tool count in log message

**Commit:** `feat(opportunity): register unified tools, update tool count`

---

## Task 8: Build, test, deploy

**Steps:**
1. `go test github.com/anatolykoptev/go_job/...` — all tests pass
2. `go vet github.com/anatolykoptev/go_job/...` — no issues
3. Docker build and deploy: `cd ~/deploy/krolik-server && docker compose build --no-cache go-job && docker compose up -d --no-deps --force-recreate go-job`
4. Verify MCP tools via `claude mcp list`

**Commit:** none (deploy step)

---
