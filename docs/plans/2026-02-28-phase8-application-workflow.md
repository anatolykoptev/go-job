# Phase 8: Application Workflow — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add 3 tools that streamline the apply→offer→negotiate pipeline: `application_prep` (one-call combo), `offer_compare` (side-by-side offer analysis), `negotiation_prep` (salary negotiation scripts).

**Architecture:** All 3 are LLM prompt-based, same pattern as Phase 7. `application_prep` orchestrates existing functions (AnalyzeResume, GenerateCoverLetter, PrepareInterview) into a single call. `offer_compare` and `negotiation_prep` are pure LLM — no external enrichment needed. Optional `ResearchSalary` enrichment in `negotiation_prep`.

**Tech Stack:** Go, MCP go-sdk, existing `engine.CallLLM`, existing resume/interview/salary functions.

---

## Files to Create

| File | Contents |
|------|----------|
| `internal/engine/jobs/application.go` | `ApplicationPrepResult`, `PrepareApplication()` |
| `internal/engine/jobs/offer.go` | `OfferCompareResult`, `CompareOffers()` |
| `internal/engine/jobs/negotiation.go` | `NegotiationPrepResult`, `PrepareNegotiation()` |
| `internal/jobserver/tool_application.go` | `registerApplicationPrep()` |
| `internal/jobserver/tool_offer.go` | `registerOfferCompare()` |
| `internal/jobserver/tool_negotiation.go` | `registerNegotiationPrep()` |

## Files to Modify

| File | Change |
|------|--------|
| `internal/engine/types_jobs.go` | Add 3 input structs |
| `internal/jobserver/register.go` | Add 3 `registerXxx(server)` calls |

---

### Task 1: Add input structs to `types_jobs.go`

**Files:**
- Modify: `internal/engine/types_jobs.go` — append after `SkillGapInput`

**Step 1: Add 3 input structs**

```go
// ApplicationPrepInput is the input for application_prep.
type ApplicationPrepInput struct {
	Resume         string `json:"resume" jsonschema:"Your resume text"`
	JobDescription string `json:"job_description" jsonschema:"Job description to apply for"`
	Company        string `json:"company,omitempty" jsonschema:"Company name (enriches with company research)"`
	Tone           string `json:"tone,omitempty" jsonschema:"Cover letter tone: professional (default), friendly, concise"`
}

// OfferCompareInput is the input for offer_compare.
type OfferCompareInput struct {
	Offers     string `json:"offers" jsonschema:"Describe 2+ job offers to compare (company, role, salary, equity, benefits, WLB, growth, location)"`
	Priorities string `json:"priorities,omitempty" jsonschema:"Your priorities: e.g. salary, remote, growth, WLB (helps weight the comparison)"`
}

// NegotiationPrepInput is the input for negotiation_prep.
type NegotiationPrepInput struct {
	Role       string `json:"role" jsonschema:"Job title you are negotiating for"`
	Company    string `json:"company,omitempty" jsonschema:"Company name (enriches with salary research data)"`
	Location   string `json:"location,omitempty" jsonschema:"Job location (for salary benchmarks)"`
	CurrentOffer string `json:"current_offer" jsonschema:"Current offer details: salary, equity, benefits, signing bonus"`
	TargetComp   string `json:"target_comp,omitempty" jsonschema:"Your target compensation (what you want to negotiate to)"`
	Leverage     string `json:"leverage,omitempty" jsonschema:"Your leverage: competing offers, unique skills, market demand"`
}
```

**Step 2: Verify build**

Run: `cd ~/src/go-job && go build -buildvcs=false ./...`
Expected: exit 0

**Step 3: Commit**

```bash
git add internal/engine/types_jobs.go
git commit -m "feat(phase8): add input structs for application_prep, offer_compare, negotiation_prep"
```

---

### Task 2: Implement `application_prep` engine + tool

**Files:**
- Create: `internal/engine/jobs/application.go`
- Create: `internal/jobserver/tool_application.go`

**Step 1: Create `internal/engine/jobs/application.go`**

This tool orchestrates 3 existing functions in parallel (resume analysis, cover letter, interview prep) and returns a combined package.

```go
package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// ApplicationPrepResult is a complete application package.
type ApplicationPrepResult struct {
	Analysis     *ResumeAnalysisResult `json:"analysis"`
	CoverLetter  *CoverLetterResult    `json:"cover_letter"`
	InterviewPrep *InterviewPrepResult  `json:"interview_prep"`
	CompanyInfo  *CompanyResearchResult `json:"company_info,omitempty"`
	Summary      string                `json:"summary"`
}

// PrepareApplication generates a complete application package:
// resume analysis + cover letter + interview prep + optional company research.
func PrepareApplication(ctx context.Context, resume, jobDescription, company, tone string) (*ApplicationPrepResult, error) {
	if tone == "" {
		tone = "professional"
	}

	resumeTrunc := engine.TruncateRunes(resume, 4000, "")
	jdTrunc := engine.TruncateRunes(jobDescription, 3000, "")

	var result ApplicationPrepResult
	var mu sync.Mutex
	var wg sync.WaitGroup
	var firstErr error

	setErr := func(err error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		mu.Unlock()
	}

	// 1. Resume analysis
	wg.Add(1)
	go func() {
		defer wg.Done()
		analysis, err := AnalyzeResume(ctx, resumeTrunc, jdTrunc)
		if err != nil {
			slog.Warn("application_prep: resume analysis failed", slog.Any("error", err))
			setErr(fmt.Errorf("resume analysis: %w", err))
			return
		}
		mu.Lock()
		result.Analysis = analysis
		mu.Unlock()
	}()

	// 2. Cover letter
	wg.Add(1)
	go func() {
		defer wg.Done()
		cl, err := GenerateCoverLetter(ctx, resumeTrunc, jdTrunc, tone)
		if err != nil {
			slog.Warn("application_prep: cover letter failed", slog.Any("error", err))
			setErr(fmt.Errorf("cover letter: %w", err))
			return
		}
		mu.Lock()
		result.CoverLetter = cl
		mu.Unlock()
	}()

	// 3. Interview prep
	wg.Add(1)
	go func() {
		defer wg.Done()
		ip, err := PrepareInterview(ctx, resumeTrunc, jdTrunc, company, "all")
		if err != nil {
			slog.Warn("application_prep: interview prep failed", slog.Any("error", err))
			setErr(fmt.Errorf("interview prep: %w", err))
			return
		}
		mu.Lock()
		result.InterviewPrep = ip
		mu.Unlock()
	}()

	// 4. Optional company research
	if company != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cr, err := ResearchCompany(ctx, company)
			if err != nil {
				slog.Warn("application_prep: company research failed", slog.Any("error", err))
				return // non-fatal
			}
			mu.Lock()
			result.CompanyInfo = cr
			mu.Unlock()
		}()
	}

	wg.Wait()

	if firstErr != nil {
		return nil, fmt.Errorf("application_prep: %w", firstErr)
	}

	// Build summary from sub-results
	var parts []string
	if result.Analysis != nil {
		parts = append(parts, fmt.Sprintf("ATS Score: %d/100.", result.Analysis.ATSScore))
	}
	if result.CoverLetter != nil {
		parts = append(parts, fmt.Sprintf("Cover letter: %d words (%s tone).", result.CoverLetter.WordCount, result.CoverLetter.Tone))
	}
	if result.InterviewPrep != nil {
		parts = append(parts, fmt.Sprintf("Interview prep: %d questions generated.", len(result.InterviewPrep.Questions)))
	}
	if result.CompanyInfo != nil {
		parts = append(parts, fmt.Sprintf("Company research: %s.", result.CompanyInfo.Name))
	}
	result.Summary = strings.Join(parts, " ")

	return &result, nil
}
```

**Step 2: Create `internal/jobserver/tool_application.go`**

```go
package jobserver

import (
	"context"
	"errors"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerApplicationPrep(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "application_prep",
		Description: "Generate a complete application package in one call: ATS resume analysis, tailored cover letter, interview prep questions with model answers, and optional company research. Combines resume_analyze + cover_letter_generate + interview_prep into a single workflow.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.ApplicationPrepInput) (*mcp.CallToolResult, *jobs.ApplicationPrepResult, error) {
		if input.Resume == "" {
			return nil, nil, errors.New("resume is required")
		}
		if input.JobDescription == "" {
			return nil, nil, errors.New("job_description is required")
		}
		result, err := jobs.PrepareApplication(ctx, input.Resume, input.JobDescription, input.Company, input.Tone)
		if err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	})
}
```

**Step 3: Verify build**

Run: `cd ~/src/go-job && go build -buildvcs=false ./...`
Expected: exit 0

**Step 4: Commit**

```bash
git add internal/engine/jobs/application.go internal/jobserver/tool_application.go
git commit -m "feat(phase8): add application_prep — one-call application package"
```

---

### Task 3: Implement `offer_compare` engine + tool

**Files:**
- Create: `internal/engine/jobs/offer.go`
- Create: `internal/jobserver/tool_offer.go`

**Step 1: Create `internal/engine/jobs/offer.go`**

Pure LLM tool — no external enrichment.

```go
package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// OfferItem is a single offer in a comparison.
type OfferItem struct {
	Company    string `json:"company"`
	Role       string `json:"role"`
	TotalComp  string `json:"total_comp"`
	Pros       []string `json:"pros"`
	Cons       []string `json:"cons"`
	Score      int    `json:"score"` // 0-100 overall score
}

// OfferCompareResult is the structured output of offer_compare.
type OfferCompareResult struct {
	Offers         []OfferItem `json:"offers"`
	Recommendation string      `json:"recommendation"`
	Comparison     string      `json:"comparison"` // side-by-side analysis
	Summary        string      `json:"summary"`
}

const offerComparePrompt = `You are an expert career advisor specializing in job offer evaluation and comparison.

Analyze and compare the following job offers. Consider all dimensions: compensation (base, equity, bonus), benefits (health, PTO, 401k), work-life balance, growth potential, company stability, remote policy, location, and career trajectory.

OFFERS:
%s
%s
Provide a thorough comparison:

1. For each offer, calculate the estimated total annual compensation (base + equity/year + bonus) and list pros and cons.
2. Score each offer 0-100 based on overall value (not just salary).
3. Write a side-by-side comparison covering: compensation, benefits, WLB, growth, stability.
4. Give a clear recommendation with reasoning.
5. Write a brief summary.

Return a JSON object with this exact structure:
{
  "offers": [
    {
      "company": "<company name>",
      "role": "<role title>",
      "total_comp": "<estimated total annual compensation>",
      "pros": ["<pro 1>", "<pro 2>"],
      "cons": ["<con 1>", "<con 2>"],
      "score": <0-100>
    }
  ],
  "recommendation": "<which offer to accept and why, 2-3 sentences>",
  "comparison": "<detailed side-by-side analysis, 4-6 sentences covering comp, benefits, WLB, growth>",
  "summary": "<1-2 sentence bottom line>"
}

Return ONLY the JSON object, no markdown, no explanation.`

// CompareOffers compares multiple job offers and recommends the best choice.
func CompareOffers(ctx context.Context, offers, priorities string) (*OfferCompareResult, error) {
	offersTrunc := engine.TruncateRunes(offers, 5000, "")

	var priorityContext string
	if priorities != "" {
		priorityContext = fmt.Sprintf("CANDIDATE PRIORITIES: %s\nWeight the comparison and scoring toward these priorities.\n", priorities)
	}

	prompt := fmt.Sprintf(offerComparePrompt, offersTrunc, priorityContext)
	raw, err := engine.CallLLM(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("offer_compare LLM: %w", err)
	}

	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var result OfferCompareResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("offer_compare parse: %w (raw: %s)", err, engine.TruncateRunes(raw, 200, "..."))
	}
	return &result, nil
}
```

**Step 2: Create `internal/jobserver/tool_offer.go`**

```go
package jobserver

import (
	"context"
	"errors"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerOfferCompare(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "offer_compare",
		Description: "Compare multiple job offers side-by-side across compensation, benefits, work-life balance, growth potential, and stability. Scores each offer 0-100 and recommends the best choice based on your priorities.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.OfferCompareInput) (*mcp.CallToolResult, *jobs.OfferCompareResult, error) {
		if input.Offers == "" {
			return nil, nil, errors.New("offers is required")
		}
		result, err := jobs.CompareOffers(ctx, input.Offers, input.Priorities)
		if err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	})
}
```

**Step 3: Verify build**

Run: `cd ~/src/go-job && go build -buildvcs=false ./...`
Expected: exit 0

**Step 4: Commit**

```bash
git add internal/engine/jobs/offer.go internal/jobserver/tool_offer.go
git commit -m "feat(phase8): add offer_compare — side-by-side offer analysis"
```

---

### Task 4: Implement `negotiation_prep` engine + tool

**Files:**
- Create: `internal/engine/jobs/negotiation.go`
- Create: `internal/jobserver/tool_negotiation.go`

**Step 1: Create `internal/engine/jobs/negotiation.go`**

Enriches with `ResearchSalary` if company/role/location provided.

```go
package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// NegotiationPoint is a single talking point for salary negotiation.
type NegotiationPoint struct {
	Point        string `json:"point"`         // the argument to make
	Script       string `json:"script"`        // exact words to say
	Anticipation string `json:"anticipation"`  // expected counter-argument
	Response     string `json:"response"`      // how to respond to counter
}

// NegotiationPrepResult is the structured output of negotiation_prep.
type NegotiationPrepResult struct {
	MarketData      string             `json:"market_data"`       // salary benchmarks context
	OpeningScript   string             `json:"opening_script"`    // how to open the negotiation
	TalkingPoints   []NegotiationPoint `json:"talking_points"`
	WalkAwayPoint   string             `json:"walk_away_point"`   // BATNA analysis
	ClosingScript   string             `json:"closing_script"`    // how to close/accept
	RedFlags        []string           `json:"red_flags"`         // signs the offer is below market
	Summary         string             `json:"summary"`
}

const negotiationPrepPrompt = `You are an expert salary negotiation coach. Generate a complete negotiation playbook based on the candidate's situation.

ROLE: %s
CURRENT OFFER: %s
%s%s%s
Build a comprehensive negotiation strategy:

1. Market data context — summarize what the market pays for this role (use salary research data if provided).
2. Opening script — exact words to open the negotiation conversation (professional, confident, non-confrontational).
3. Talking points — 4-6 key arguments, each with:
   - The point to make
   - Exact script (what to say word-for-word)
   - Anticipated counter-argument from the employer
   - How to respond to that counter
4. Walk-away point (BATNA) — what's the candidate's best alternative? At what point should they walk away?
5. Closing script — how to accept gracefully once terms are agreed.
6. Red flags — signs the offer or company might be problematic.
7. Brief summary of the overall strategy.

Return a JSON object with this exact structure:
{
  "market_data": "<salary benchmarks and market context>",
  "opening_script": "<exact opening words for the negotiation, 3-4 sentences>",
  "talking_points": [
    {
      "point": "<argument summary>",
      "script": "<exact words to say>",
      "anticipation": "<likely employer counter>",
      "response": "<how to respond>"
    }
  ],
  "walk_away_point": "<BATNA analysis and walk-away threshold>",
  "closing_script": "<how to accept and close, 2-3 sentences>",
  "red_flags": ["<red flag 1>", "<red flag 2>"],
  "summary": "<overall negotiation strategy, 2-3 sentences>"
}

Return ONLY the JSON object, no markdown, no explanation.`

// PrepareNegotiation generates a salary negotiation playbook.
// If role+location provided, enriches with salary research data.
func PrepareNegotiation(ctx context.Context, role, company, location, currentOffer, targetComp, leverage string) (*NegotiationPrepResult, error) {
	// Optional salary research enrichment
	var salaryContext string
	if role != "" && location != "" {
		res, err := ResearchSalary(ctx, role, location, "")
		if err != nil {
			slog.Warn("negotiation_prep: salary research failed, proceeding without", slog.Any("error", err))
		} else {
			salaryContext = fmt.Sprintf("SALARY RESEARCH (%s in %s):\np25: %d, median: %d, p75: %d %s\nSources: %s\n\n",
				res.Role, res.Location, res.P25, res.Median, res.P75, res.Currency,
				strings.Join(res.Sources, ", "))
		}
	}

	var companyLine string
	if company != "" {
		companyLine = fmt.Sprintf("COMPANY: %s\n", company)
	}

	var targetLine string
	if targetComp != "" {
		targetLine = fmt.Sprintf("TARGET COMPENSATION: %s\n", targetComp)
	}

	var leverageLine string
	if leverage != "" {
		leverageLine = fmt.Sprintf("CANDIDATE LEVERAGE: %s\n", leverage)
	}

	prompt := fmt.Sprintf(negotiationPrepPrompt,
		role, currentOffer,
		companyLine, salaryContext,
		targetLine+leverageLine,
	)

	raw, err := engine.CallLLM(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("negotiation_prep LLM: %w", err)
	}

	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var result NegotiationPrepResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("negotiation_prep parse: %w (raw: %s)", err, engine.TruncateRunes(raw, 200, "..."))
	}
	return &result, nil
}
```

**Step 2: Create `internal/jobserver/tool_negotiation.go`**

```go
package jobserver

import (
	"context"
	"errors"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerNegotiationPrep(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "negotiation_prep",
		Description: "Generate a salary negotiation playbook with market data, opening/closing scripts, talking points with anticipated counters, BATNA analysis, and red flags. Optionally enriches with salary research benchmarks.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.NegotiationPrepInput) (*mcp.CallToolResult, *jobs.NegotiationPrepResult, error) {
		if input.Role == "" {
			return nil, nil, errors.New("role is required")
		}
		if input.CurrentOffer == "" {
			return nil, nil, errors.New("current_offer is required")
		}
		result, err := jobs.PrepareNegotiation(ctx, input.Role, input.Company, input.Location, input.CurrentOffer, input.TargetComp, input.Leverage)
		if err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	})
}
```

**Step 3: Verify build**

Run: `cd ~/src/go-job && go build -buildvcs=false ./...`
Expected: exit 0

**Step 4: Commit**

```bash
git add internal/engine/jobs/negotiation.go internal/jobserver/tool_negotiation.go
git commit -m "feat(phase8): add negotiation_prep — salary negotiation playbook"
```

---

### Task 5: Register all 3 tools + final verification

**Files:**
- Modify: `internal/jobserver/register.go`

**Step 1: Add registration calls**

Add under the `// Interview & Career Prep` section, after `registerSkillGap(server)`:

```go
	// Application Workflow
	registerApplicationPrep(server)
	registerOfferCompare(server)
	registerNegotiationPrep(server)
```

**Step 2: Build + vet + test**

Run:
```bash
cd ~/src/go-job
go build -buildvcs=false ./...
go vet -buildvcs=false ./...
go test -buildvcs=false ./... -count=1 -short
```
Expected: all exit 0, all tests pass

**Step 3: Commit**

```bash
git add internal/jobserver/register.go
git commit -m "feat(phase8): register application_prep, offer_compare, negotiation_prep"
```

---

### Task 6: Deploy and smoke test

**Step 1: Fix file ownership**

```bash
sudo chown krolik:krolik \
  internal/engine/types_jobs.go \
  internal/engine/jobs/application.go \
  internal/engine/jobs/offer.go \
  internal/engine/jobs/negotiation.go \
  internal/jobserver/register.go \
  internal/jobserver/tool_application.go \
  internal/jobserver/tool_offer.go \
  internal/jobserver/tool_negotiation.go
```

**Step 2: Deploy**

```bash
cd ~/deploy/krolik-server
docker compose build --no-cache go-job
docker compose up -d --no-deps --force-recreate go-job
```

**Step 3: Verify**

```bash
# Health check
curl -s http://127.0.0.1:8891/health
# Expected: {"status":"ok","service":"go_job","version":"dev"}

# Tool count (should be 25 = 22 + 3)
docker compose logs go-job 2>&1 | grep "tools registered"
# Expected: count=25
```

---

## Execution Plan

3 independent tools → parallel subagents:

1. **Agent A**: Task 2 — `application.go` + `tool_application.go`
2. **Agent B**: Task 3 — `offer.go` + `tool_offer.go`
3. **Agent C**: Task 4 — `negotiation.go` + `tool_negotiation.go`
4. **Main**: Task 1 (types) + Task 5 (register) + Task 6 (deploy)
