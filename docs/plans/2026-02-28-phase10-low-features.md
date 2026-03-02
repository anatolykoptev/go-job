# Phase 10 Low Features Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add 5 low-effort UX/source improvements: Google Jobs source, pagination offset, results_limit param, user profile config, and blacklist filter.

**Architecture:** All changes modify the existing `job_search` pipeline. Google Jobs is a new SearXNG-based source. Pagination/limit/blacklist are params added to `JobSearchInput` and applied in the aggregation layer of `tool_job_search.go`. User profile is a JSON file at `~/.go_job/profile.json` loaded at startup.

**Tech Stack:** Go, SearXNG, existing engine pipeline, JSON file storage.

---

### Task 1: Add `results_limit` param

The simplest change — add a `Limit` field to `JobSearchInput` and use it instead of hardcoded `15` in `tool_job_search.go`.

**Files:**
- Modify: `internal/engine/types_jobs.go` — add `Limit` field to `JobSearchInput`
- Modify: `internal/jobserver/tool_job_search.go:204-207` — use `input.Limit` instead of `15`

**Step 1: Add field to `JobSearchInput`**

In `internal/engine/types_jobs.go`, add after the `Language` field (line 16):

```go
	Limit    int    `json:"limit,omitempty" jsonschema:"Max results to return (default 15, max 50)"`
```

**Step 2: Use limit in tool_job_search.go**

In `internal/jobserver/tool_job_search.go`, after `platform` normalization (after line 47), add:

```go
	limit := input.Limit
	if limit <= 0 {
		limit = 15
	}
	if limit > 50 {
		limit = 50
	}
```

Then replace lines 204-207:

```go
	top := engine.DedupByDomain(deduped, 15)
	if len(top) > 15 {
		top = top[:15]
	}
```

With:

```go
	top := engine.DedupByDomain(deduped, limit)
	if len(top) > limit {
		top = top[:limit]
	}
```

Also update `cacheKey` (line 37) to include limit:

```go
	cacheKey := engine.CacheKey("job_search", input.Query, input.Location, input.Experience, input.JobType, input.Remote, input.TimeRange, input.Platform, fmt.Sprintf("limit_%d", limit))
```

Add `"fmt"` to imports if not already present.

**Step 3: Update tool description**

In `tool_job_search.go` line 30, update Description to mention limit parameter.

**Step 4: Verify**

```bash
cd ~/src/go-job && go build -buildvcs=false ./... && go vet -buildvcs=false ./...
```

**Step 5: Commit**

```bash
git add internal/engine/types_jobs.go internal/jobserver/tool_job_search.go
git commit -m "feat: add results_limit param to job_search (default 15, max 50)"
```

---

### Task 2: Add pagination `offset` param

Add an `Offset` field that skips N results after dedup, enabling pagination.

**Files:**
- Modify: `internal/engine/types_jobs.go` — add `Offset` field to `JobSearchInput`
- Modify: `internal/jobserver/tool_job_search.go` — apply offset before limit

**Step 1: Add field to `JobSearchInput`**

In `internal/engine/types_jobs.go`, add after `Limit`:

```go
	Offset   int    `json:"offset,omitempty" jsonschema:"Skip first N results for pagination (default 0)"`
```

**Step 2: Apply offset in tool_job_search.go**

After dedup pass 2 (after `deduped = canonDeduped`, line 202), before the DedupByDomain call, add:

```go
	// Apply pagination offset.
	if input.Offset > 0 && input.Offset < len(deduped) {
		deduped = deduped[input.Offset:]
	} else if input.Offset >= len(deduped) {
		return nil, engine.JobSearchOutput{Query: input.Query, Summary: "No more results (offset beyond total)."}, nil
	}
```

Also update `cacheKey` to include offset:

```go
	cacheKey := engine.CacheKey("job_search", input.Query, input.Location, input.Experience, input.JobType, input.Remote, input.TimeRange, input.Platform, fmt.Sprintf("limit_%d_offset_%d", limit, input.Offset))
```

**Step 3: Verify**

```bash
cd ~/src/go-job && go build -buildvcs=false ./... && go vet -buildvcs=false ./...
```

**Step 4: Commit**

```bash
git add internal/engine/types_jobs.go internal/jobserver/tool_job_search.go
git commit -m "feat: add offset param to job_search for pagination"
```

---

### Task 3: Add blacklist filter

Filter out jobs by company name or keywords before results are shown. Blacklist passed as comma-separated strings.

**Files:**
- Modify: `internal/engine/types_jobs.go` — add `Blacklist` field
- Modify: `internal/jobserver/tool_job_search.go` — filter after dedup, before offset/limit

**Step 1: Add field to `JobSearchInput`**

In `internal/engine/types_jobs.go`, add after `Offset`:

```go
	Blacklist string `json:"blacklist,omitempty" jsonschema:"Comma-separated company names or keywords to exclude from results (e.g. Google, Meta, staffing agency)"`
```

**Step 2: Add filter function in tool_job_search.go**

Add a helper function after `buildJobSearxQuery`:

```go
func applyBlacklist(results []engine.SearxngResult, blacklist string) []engine.SearxngResult {
	if blacklist == "" {
		return results
	}
	var terms []string
	for _, t := range strings.Split(blacklist, ",") {
		t = strings.ToLower(strings.TrimSpace(t))
		if t != "" {
			terms = append(terms, t)
		}
	}
	if len(terms) == 0 {
		return results
	}
	var filtered []engine.SearxngResult
	for _, r := range results {
		lower := strings.ToLower(r.Title + " " + r.Content)
		blocked := false
		for _, term := range terms {
			if strings.Contains(lower, term) {
				blocked = true
				break
			}
		}
		if !blocked {
			filtered = append(filtered, r)
		}
	}
	return filtered
}
```

**Step 3: Apply filter after dedup pass 2**

After `deduped = canonDeduped` (line 202), before offset:

```go
	// Apply blacklist filter.
	deduped = applyBlacklist(deduped, input.Blacklist)
```

**Step 4: Verify**

```bash
cd ~/src/go-job && go build -buildvcs=false ./... && go vet -buildvcs=false ./...
```

**Step 5: Commit**

```bash
git add internal/engine/types_jobs.go internal/jobserver/tool_job_search.go
git commit -m "feat: add blacklist filter to job_search (comma-separated exclusions)"
```

---

### Task 4: Add Google Jobs source

Use SearXNG with `site:careers.google.com` query pattern. Simple — follows existing SearXNG fallback approach (no separate source file needed).

**Files:**
- Modify: `internal/jobserver/tool_job_search.go` — add `google` platform option and SearXNG query

**Step 1: Add platform constant**

In `tool_job_search.go`, add to the const block (after line 23):

```go
	platGoogle = "google"
```

**Step 2: Add source selection**

After `useTwitter` (line 56), add:

```go
	useGoogle := platform == platAll || platform == platGoogle
```

After the twitter `srcs = append` block (line 89), add:

```go
	if useGoogle {
		srcs = append(srcs, platGoogle)
	}
```

**Step 3: Add source dispatch**

In the switch statement (after the `twitter` case, before the closing `}`), add:

```go
			case platGoogle:
				searxQuery := input.Query + " " + input.Location + " site:careers.google.com OR site:jobs.google.com"
				results, err := engine.SearchSearXNG(ctx, searxQuery, lang, input.TimeRange, "google")
				if err != nil {
					slog.Warn("job_search: google error", slog.Any("error", err))
				}
				ch <- sourceResult{name: name, results: results, err: err}
```

**Step 4: Update `buildJobSearxQuery`**

Add a case in the switch (before `default`):

```go
	case platGoogle:
		sitePart = "site:careers.google.com OR site:jobs.google.com"
```

**Step 5: Update tool description and Platform jsonschema**

In `types_jobs.go`, update the `Platform` field description to include `google`:

```go
	Platform   string `json:"platform,omitempty" jsonschema:"Source filter: linkedin, greenhouse, lever, ats (greenhouse+lever), yc (workatastartup.com), hn (HN Who is Hiring), indeed, habr (Хабр Карьера), twitter (X/Twitter job tweets), google (Google Jobs), startup (yc+hn+ats), all (default)"`
```

**Step 6: Verify**

```bash
cd ~/src/go-job && go build -buildvcs=false ./... && go vet -buildvcs=false ./...
```

**Step 7: Commit**

```bash
git add internal/engine/types_jobs.go internal/jobserver/tool_job_search.go
git commit -m "feat: add Google Jobs source via SearXNG (site:careers.google.com)"
```

---

### Task 5: Add user profile (`~/.go_job/profile.json`)

A JSON file storing user preferences: default blacklist, preferred platforms, default limit. Loaded once at startup, applied as defaults in job_search.

**Files:**
- Create: `internal/engine/jobs/profile.go` — profile struct + Load/Save functions
- Modify: `internal/jobserver/tool_job_search.go` — apply profile defaults

**Step 1: Create `internal/engine/jobs/profile.go`**

```go
package jobs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// UserProfile stores user preferences for job search.
type UserProfile struct {
	Blacklist        string `json:"blacklist,omitempty"`         // default blacklist (comma-separated)
	DefaultPlatform  string `json:"default_platform,omitempty"` // default platform filter
	DefaultLimit     int    `json:"default_limit,omitempty"`    // default results limit
	DefaultLocation  string `json:"default_location,omitempty"` // default location
	DefaultRemote    string `json:"default_remote,omitempty"`   // default remote preference
}

var (
	cachedProfile *UserProfile
	profileOnce   sync.Once
)

// LoadProfile loads user profile from ~/.go_job/profile.json.
// Returns empty profile if file doesn't exist. Cached after first load.
func LoadProfile() *UserProfile {
	profileOnce.Do(func() {
		cachedProfile = &UserProfile{}
		home, err := os.UserHomeDir()
		if err != nil {
			return
		}
		data, err := os.ReadFile(filepath.Join(home, ".go_job", "profile.json"))
		if err != nil {
			return // file doesn't exist yet — empty defaults
		}
		_ = json.Unmarshal(data, cachedProfile)
	})
	return cachedProfile
}

// SaveProfile writes user profile to ~/.go_job/profile.json.
func SaveProfile(p *UserProfile) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".go_job")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "profile.json"), data, 0o600)
}
```

**Step 2: Apply profile defaults in tool_job_search.go**

At the top of the handler function (after cache check, before platform normalization), add:

```go
	// Apply user profile defaults.
	profile := jobs.LoadProfile()
	if input.Platform == "" && profile.DefaultPlatform != "" {
		input.Platform = profile.DefaultPlatform
	}
	if input.Limit <= 0 && profile.DefaultLimit > 0 {
		input.Limit = profile.DefaultLimit
	}
	if input.Location == "" && profile.DefaultLocation != "" {
		input.Location = profile.DefaultLocation
	}
	if input.Remote == "" && profile.DefaultRemote != "" {
		input.Remote = profile.DefaultRemote
	}
	if input.Blacklist == "" && profile.Blacklist != "" {
		input.Blacklist = profile.Blacklist
	}
```

Note: profile defaults only apply when user didn't specify the field. Explicit input always wins.

**Step 3: Verify**

```bash
cd ~/src/go-job && go build -buildvcs=false ./... && go vet -buildvcs=false ./...
```

**Step 4: Commit**

```bash
git add internal/engine/jobs/profile.go internal/jobserver/tool_job_search.go
git commit -m "feat: add user profile (~/.go_job/profile.json) with search defaults"
```

---

## Execution Plan

Tasks 1-3 modify the same files sequentially (types_jobs.go + tool_job_search.go), so they must be sequential. Task 4 also modifies those files. Task 5 creates a new file + modifies tool_job_search.go.

**Recommended order:** Task 1 → 2 → 3 → 4 → 5 (sequential, each builds on prior).

All 5 are small — single subagent can do all sequentially, or batch as one agent.

## Verification

```bash
cd ~/src/go-job
go build -buildvcs=false ./...
go vet -buildvcs=false ./...
go test -buildvcs=false ./... -count=1 -short

# Deploy
cd ~/deploy/krolik-server
docker compose build --no-cache go-job && docker compose up -d --no-deps --force-recreate go-job
curl http://127.0.0.1:8891/health
```
