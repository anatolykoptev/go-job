# Security Bug Bounty & Freelance Job Monitors Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add security bug bounty program monitoring (HackerOne/Bugcrowd/Intigriti/YesWeHack/Immunefi) and freelance job monitoring (RemoteOK/Himalayas) to go-job with Telegram notifications.

**Architecture:** Two new data pipelines following the existing bounty scraper pattern (cache check → HTTP fetch → parse → store). Security programs come from `arkadiyt/bounty-targets-data` GitHub repo (aggregated JSON, updated hourly) plus Immunefi's direct API. Freelance jobs come from RemoteOK and Himalayas public JSON APIs. Both pipelines have background monitors with Telegram alerts.

**Tech Stack:** Go, net/http, encoding/json, existing engine cache (Redis), existing vaelor notify, existing proxy (engine.Cfg.HTTPClient)

**Build command:** `go test github.com/anatolykoptev/go_job/internal/engine/jobs -run <TestName> -v`
**Full build:** `go build github.com/anatolykoptev/go_job/...`

---

## Task 1: Add SecurityProgram and FreelanceJob types

**Files:**
- Modify: `internal/engine/types_jobs.go`

**Context:** All types live in the `engine` package. Follow the pattern of `BountyListing` struct. Keep the file under 200 lines — check current length first and split if needed.

**Implementation:**

Add these types after the `CompetingPR` struct:

```go
// SecurityProgram represents a bug bounty program from platforms like HackerOne, Bugcrowd, etc.
type SecurityProgram struct {
	Name       string   `json:"name"`
	Platform   string   `json:"platform"`    // hackerone, bugcrowd, intigriti, yeswehack, immunefi
	URL        string   `json:"url"`
	MaxBounty  string   `json:"max_bounty"`  // e.g. "$50,000"
	MinBounty  string   `json:"min_bounty"`  // e.g. "$100"
	Targets    []string `json:"targets"`     // in-scope domains/apps
	Type       string   `json:"type"`        // bug_bounty, vdp
	Managed    bool     `json:"managed"`     // managed/triaged by platform
}

// FreelanceJob represents a remote job or freelance gig.
type FreelanceJob struct {
	Title     string `json:"title"`
	Company   string `json:"company"`
	URL       string `json:"url"`
	Tags      []string `json:"tags"`
	SalaryMin int    `json:"salary_min,omitempty"`
	SalaryMax int    `json:"salary_max,omitempty"`
	Source    string `json:"source"` // remoteok, himalayas
	Posted    string `json:"posted"`
	Location  string `json:"location,omitempty"`
}
```

If `types_jobs.go` exceeds 200 lines after this addition, split security/freelance types into `types_security.go` and `types_freelance.go` in the same `engine` package.

**Verify:** `go build github.com/anatolykoptev/go_job/...`

**Commit:** `git commit -m "feat: add SecurityProgram and FreelanceJob types"`

---

## Task 2: Security bounty scraper — bounty-targets-data

**Files:**
- Create: `internal/engine/jobs/security_bounty.go`
- Create: `internal/engine/jobs/security_bounty_test.go`

**Context:** The `arkadiyt/bounty-targets-data` repo on GitHub publishes aggregated JSON for 5 platforms at raw URLs like:
- `https://raw.githubusercontent.com/arkadiyt/bounty-targets-data/main/data/hackerone_data.json`
- `https://raw.githubusercontent.com/arkadiyt/bounty-targets-data/main/data/bugcrowd_data.json`
- `https://raw.githubusercontent.com/arkadiyt/bounty-targets-data/main/data/intigriti_data.json`
- `https://raw.githubusercontent.com/arkadiyt/bounty-targets-data/main/data/yeswehack_data.json`

Each has different JSON structure. We parse all into `[]engine.SecurityProgram`.

**Test first:**

```go
package jobs

import (
	"testing"
)

func TestParseHackerOnePrograms(t *testing.T) {
	t.Parallel()
	data := []byte(`[{
		"name": "Test Program",
		"handle": "test-program",
		"url": "https://hackerone.com/test-program",
		"offers_bounties": true,
		"targets": {"in_scope": [{"asset_identifier": "*.example.com", "asset_type": "URL"}]}
	}]`)
	programs, err := parseHackerOneData(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(programs) != 1 {
		t.Fatalf("expected 1 program, got %d", len(programs))
	}
	if programs[0].Name != "Test Program" {
		t.Errorf("name = %q, want %q", programs[0].Name, "Test Program")
	}
	if programs[0].Platform != "hackerone" {
		t.Errorf("platform = %q, want %q", programs[0].Platform, "hackerone")
	}
	if len(programs[0].Targets) != 1 || programs[0].Targets[0] != "*.example.com" {
		t.Errorf("targets = %v, want [*.example.com]", programs[0].Targets)
	}
}

func TestParseBugcrowdPrograms(t *testing.T) {
	t.Parallel()
	data := []byte(`[{
		"name": "Bugcrowd Test",
		"url": "https://bugcrowd.com/test",
		"max_payout": 5000,
		"targets": {"in_scope": [{"target": "app.example.com", "type": "website"}]}
	}]`)
	programs, err := parseBugcrowdData(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(programs) != 1 {
		t.Fatalf("expected 1 program, got %d", len(programs))
	}
	if programs[0].Platform != "bugcrowd" {
		t.Errorf("platform = %q, want %q", programs[0].Platform, "bugcrowd")
	}
	if programs[0].MaxBounty != "$5,000" {
		t.Errorf("max_bounty = %q, want %q", programs[0].MaxBounty, "$5,000")
	}
}

func TestParseYesWeHackPrograms(t *testing.T) {
	t.Parallel()
	data := []byte(`[{
		"title": "YWH Test",
		"slug": "ywh-test",
		"disabled": false,
		"min_bounty": 50,
		"max_bounty": 10000,
		"scopes": [{"scope": "*.ywh.example.com"}]
	}]`)
	programs, err := parseYesWeHackData(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(programs) != 1 {
		t.Fatalf("expected 1 program, got %d", len(programs))
	}
	if programs[0].Name != "YWH Test" {
		t.Errorf("name = %q, want %q", programs[0].Name, "YWH Test")
	}
	if programs[0].MinBounty != "$50" {
		t.Errorf("min = %q, want $50", programs[0].MinBounty)
	}
}

func TestParseSecurityPrograms_empty(t *testing.T) {
	t.Parallel()
	programs, err := parseHackerOneData([]byte(`[]`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(programs) != 0 {
		t.Fatalf("expected 0 programs, got %d", len(programs))
	}
}
```

**Implementation** (`security_bounty.go`):

```go
package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/anatolykoptev/go_job/internal/engine"
)

const (
	btdBaseURL             = "https://raw.githubusercontent.com/arkadiyt/bounty-targets-data/main/data/"
	securityScrapeCacheKey = "security_programs"
)

// SearchSecurityPrograms returns aggregated bug bounty programs from all platforms.
func SearchSecurityPrograms(ctx context.Context, limit int) ([]engine.SecurityProgram, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	if cached, ok := engine.CacheLoadJSON[[]engine.SecurityProgram](ctx, securityScrapeCacheKey); ok {
		slog.Debug("security: using cached", slog.Int("count", len(cached)))
		if len(cached) > limit {
			cached = cached[:limit]
		}
		return cached, nil
	}

	programs, err := fetchAllSecurityPrograms(ctx)
	if err != nil {
		return nil, err
	}

	engine.CacheStoreJSON(ctx, securityScrapeCacheKey, "", programs)
	if len(programs) > limit {
		programs = programs[:limit]
	}
	slog.Debug("security: fetch complete", slog.Int("count", len(programs)))
	return programs, nil
}

func fetchAllSecurityPrograms(ctx context.Context) ([]engine.SecurityProgram, error) {
	type source struct {
		file  string
		parse func([]byte) ([]engine.SecurityProgram, error)
	}
	sources := []source{
		{"hackerone_data.json", parseHackerOneData},
		{"bugcrowd_data.json", parseBugcrowdData},
		{"intigriti_data.json", parseIntigritiData},
		{"yeswehack_data.json", parseYesWeHackData},
	}

	var all []engine.SecurityProgram
	var lastErr error
	for _, s := range sources {
		data, err := fetchBTDFile(ctx, s.file)
		if err != nil {
			slog.Warn("security: fetch failed", slog.String("file", s.file), slog.Any("error", err))
			lastErr = err
			continue
		}
		programs, err := s.parse(data)
		if err != nil {
			slog.Warn("security: parse failed", slog.String("file", s.file), slog.Any("error", err))
			lastErr = err
			continue
		}
		all = append(all, programs...)
	}
	if len(all) == 0 && lastErr != nil {
		return nil, fmt.Errorf("all security sources failed: %w", lastErr)
	}
	return all, nil
}

func fetchBTDFile(ctx context.Context, filename string) ([]byte, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, engine.Cfg.FetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, btdBaseURL+filename, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", engine.UserAgentBot)

	resp, err := engine.Cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("btd fetch %s: %w", filename, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("btd %s returned %d", filename, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
}
```

Note: This file will be ~100 lines. The individual platform parsers go in a separate file to stay under 200 lines.

**Create** `internal/engine/jobs/security_parsers.go`:

```go
package jobs

import (
	"encoding/json"
	"fmt"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// --- HackerOne ---

type h1Program struct {
	Name          string `json:"name"`
	Handle        string `json:"handle"`
	URL           string `json:"url"`
	OffersBounties bool  `json:"offers_bounties"`
	Targets       struct {
		InScope []struct {
			AssetID   string `json:"asset_identifier"`
			AssetType string `json:"asset_type"`
		} `json:"in_scope"`
	} `json:"targets"`
}

func parseHackerOneData(data []byte) ([]engine.SecurityProgram, error) {
	var items []h1Program
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("h1 parse: %w", err)
	}
	programs := make([]engine.SecurityProgram, 0, len(items))
	for _, item := range items {
		url := item.URL
		if url == "" {
			url = "https://hackerone.com/" + item.Handle
		}
		var targets []string
		for _, t := range item.Targets.InScope {
			targets = append(targets, t.AssetID)
		}
		typ := "vdp"
		if item.OffersBounties {
			typ = "bug_bounty"
		}
		programs = append(programs, engine.SecurityProgram{
			Name: item.Name, Platform: "hackerone", URL: url,
			Targets: targets, Type: typ,
		})
	}
	return programs, nil
}

// --- Bugcrowd ---

type bcProgram struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	MaxPayout int    `json:"max_payout"`
	Targets   struct {
		InScope []struct {
			Target string `json:"target"`
		} `json:"in_scope"`
	} `json:"targets"`
}

func parseBugcrowdData(data []byte) ([]engine.SecurityProgram, error) {
	var items []bcProgram
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("bc parse: %w", err)
	}
	programs := make([]engine.SecurityProgram, 0, len(items))
	for _, item := range items {
		var targets []string
		for _, t := range item.Targets.InScope {
			targets = append(targets, t.Target)
		}
		maxBounty := ""
		if item.MaxPayout > 0 {
			maxBounty = formatCentsUSD(item.MaxPayout * 100)
		}
		programs = append(programs, engine.SecurityProgram{
			Name: item.Name, Platform: "bugcrowd", URL: item.URL,
			MaxBounty: maxBounty, Targets: targets, Type: "bug_bounty",
		})
	}
	return programs, nil
}

// --- Intigriti ---

type igProgram struct {
	Name        string `json:"name"`
	CompanyHandle string `json:"company_handle"`
	MaxBounty   struct{ Value int `json:"value"` } `json:"max_bounty"`
	MinBounty   struct{ Value int `json:"value"` } `json:"min_bounty"`
	Targets     struct {
		InScope []struct {
			Endpoint string `json:"endpoint"`
		} `json:"in_scope"`
	} `json:"targets"`
}

func parseIntigritiData(data []byte) ([]engine.SecurityProgram, error) {
	var items []igProgram
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("ig parse: %w", err)
	}
	programs := make([]engine.SecurityProgram, 0, len(items))
	for _, item := range items {
		var targets []string
		for _, t := range item.Targets.InScope {
			targets = append(targets, t.Endpoint)
		}
		url := "https://app.intigriti.com/programs/" + item.CompanyHandle
		programs = append(programs, engine.SecurityProgram{
			Name: item.Name, Platform: "intigriti", URL: url,
			MaxBounty: formatOptionalUSD(item.MaxBounty.Value),
			MinBounty: formatOptionalUSD(item.MinBounty.Value),
			Targets: targets, Type: "bug_bounty",
		})
	}
	return programs, nil
}

// --- YesWeHack ---

type ywhProgram struct {
	Title    string `json:"title"`
	Slug     string `json:"slug"`
	Disabled bool   `json:"disabled"`
	MinBounty int   `json:"min_bounty"`
	MaxBounty int   `json:"max_bounty"`
	Scopes    []struct {
		Scope string `json:"scope"`
	} `json:"scopes"`
}

func parseYesWeHackData(data []byte) ([]engine.SecurityProgram, error) {
	var items []ywhProgram
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("ywh parse: %w", err)
	}
	programs := make([]engine.SecurityProgram, 0, len(items))
	for _, item := range items {
		if item.Disabled {
			continue
		}
		var targets []string
		for _, s := range item.Scopes {
			targets = append(targets, s.Scope)
		}
		programs = append(programs, engine.SecurityProgram{
			Name: item.Title, Platform: "yeswehack",
			URL:       "https://yeswehack.com/programs/" + item.Slug,
			MinBounty: formatOptionalUSD(item.MinBounty),
			MaxBounty: formatOptionalUSD(item.MaxBounty),
			Targets:   targets, Type: "bug_bounty",
		})
	}
	return programs, nil
}

func formatOptionalUSD(dollars int) string {
	if dollars <= 0 {
		return ""
	}
	return formatCentsUSD(dollars * 100)
}
```

**Verify:** `go test github.com/anatolykoptev/go_job/internal/engine/jobs -run TestParse.*Programs -v`

**Commit:** `git commit -m "feat: add security bounty program scraper (bounty-targets-data)"`

---

## Task 3: Immunefi scraper

**Files:**
- Create: `internal/engine/jobs/immunefi.go`
- Create: `internal/engine/jobs/immunefi_test.go`

**Context:** Immunefi has a single JSON dump at `https://immunefi.com/public-api/bounties.json`. Returns array of programs with `slug`, `maxBounty`, `ecosystem`, `assets[]` (each with `url`, `type`).

**Test:**

```go
package jobs

import (
	"testing"
)

func TestParseImmunefiResponse(t *testing.T) {
	t.Parallel()
	data := []byte(`[{
		"slug": "test-protocol",
		"maxBounty": 100000,
		"ecosystem": "ethereum",
		"kyc": true,
		"assets": [
			{"url": "https://github.com/test/repo", "type": "smart_contract"},
			{"url": "https://app.test.com", "type": "websites_and_applications"}
		]
	}]`)
	programs, err := parseImmunefiResponse(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(programs) != 1 {
		t.Fatalf("expected 1 program, got %d", len(programs))
	}
	p := programs[0]
	if p.Name != "test-protocol" {
		t.Errorf("name = %q, want %q", p.Name, "test-protocol")
	}
	if p.Platform != "immunefi" {
		t.Errorf("platform = %q, want %q", p.Platform, "immunefi")
	}
	if p.MaxBounty != "$100,000" {
		t.Errorf("max = %q, want $100,000", p.MaxBounty)
	}
	if len(p.Targets) != 2 {
		t.Errorf("targets = %d, want 2", len(p.Targets))
	}
}

func TestParseImmunefiResponse_empty(t *testing.T) {
	t.Parallel()
	programs, err := parseImmunefiResponse([]byte(`[]`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(programs) != 0 {
		t.Fatalf("expected 0, got %d", len(programs))
	}
}
```

**Implementation** (`immunefi.go`):

```go
package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/anatolykoptev/go_job/internal/engine"
)

const (
	immunefiAPIURL         = "https://immunefi.com/public-api/bounties.json"
	immunefiScrapeCacheKey = "immunefi_programs"
)

type immunefiProgram struct {
	Slug      string `json:"slug"`
	MaxBounty int    `json:"maxBounty"`
	Ecosystem string `json:"ecosystem"`
	KYC       bool   `json:"kyc"`
	Assets    []struct {
		URL  string `json:"url"`
		Type string `json:"type"`
	} `json:"assets"`
}

// SearchImmunefi fetches bug bounty programs from Immunefi.
func SearchImmunefi(ctx context.Context, limit int) ([]engine.SecurityProgram, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	if cached, ok := engine.CacheLoadJSON[[]engine.SecurityProgram](ctx, immunefiScrapeCacheKey); ok {
		slog.Debug("immunefi: using cached", slog.Int("count", len(cached)))
		if len(cached) > limit {
			cached = cached[:limit]
		}
		return cached, nil
	}

	programs, err := fetchImmunefi(ctx)
	if err != nil {
		return nil, err
	}

	engine.CacheStoreJSON(ctx, immunefiScrapeCacheKey, "", programs)
	if len(programs) > limit {
		programs = programs[:limit]
	}
	slog.Debug("immunefi: fetch complete", slog.Int("count", len(programs)))
	return programs, nil
}

func fetchImmunefi(ctx context.Context) ([]engine.SecurityProgram, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, engine.Cfg.FetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, immunefiAPIURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", engine.UserAgentChrome)

	resp, err := engine.Cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("immunefi request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("immunefi returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 5*1024*1024))
	if err != nil {
		return nil, err
	}
	return parseImmunefiResponse(body)
}

func parseImmunefiResponse(data []byte) ([]engine.SecurityProgram, error) {
	var items []immunefiProgram
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("immunefi parse: %w", err)
	}
	programs := make([]engine.SecurityProgram, 0, len(items))
	for _, item := range items {
		var targets []string
		for _, a := range item.Assets {
			targets = append(targets, a.URL)
		}
		programs = append(programs, engine.SecurityProgram{
			Name:      item.Slug,
			Platform:  "immunefi",
			URL:       "https://immunefi.com/bug-bounty/" + item.Slug,
			MaxBounty: formatCentsUSD(item.MaxBounty * 100),
			Targets:   targets,
			Type:      "bug_bounty",
		})
	}
	return programs, nil
}
```

**Verify:** `go test github.com/anatolykoptev/go_job/internal/engine/jobs -run TestParseImmunefi -v`

**Commit:** `git commit -m "feat: add Immunefi bug bounty program scraper"`

---

## Task 4: Security bounty monitor

**Files:**
- Create: `internal/engine/jobs/security_monitor.go`

**Context:** Follow exact pattern of `bounty_monitor.go`. Use `SecurityProgram.URL` as unique key for seen-set. Combine programs from `SearchSecurityPrograms()` and `SearchImmunefi()`. 30-min interval. Telegram notification with program name, platform, max bounty, URL.

**Implementation:**

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

const securitySeenKey = "security_seen_ids"

// StartSecurityMonitor launches background monitoring for new security bounty programs.
func StartSecurityMonitor(ctx context.Context) {
	interval := 30 * time.Minute
	if engine.Cfg.VaelorNotifyURL == "" {
		slog.Info("security_monitor: disabled (VAELOR_NOTIFY_URL not set)")
		return
	}
	slog.Info("security_monitor: starting", slog.Duration("interval", interval))

	time.AfterFunc(45*time.Second, func() { checkNewSecurityPrograms(ctx) })

	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				slog.Info("security_monitor: stopped")
				return
			case <-ticker.C:
				checkNewSecurityPrograms(ctx)
			}
		}
	}()
}

func checkNewSecurityPrograms(ctx context.Context) {
	programs, err := SearchSecurityPrograms(ctx, 500)
	if err != nil {
		slog.Warn("security_monitor: btd fetch failed", slog.Any("error", err))
	}

	imm, immErr := SearchImmunefi(ctx, 500)
	if immErr != nil {
		slog.Warn("security_monitor: immunefi fetch failed", slog.Any("error", immErr))
	}
	programs = append(programs, imm...)

	if len(programs) == 0 {
		if err != nil || immErr != nil {
			slog.Warn("security_monitor: all sources failed")
		}
		return
	}

	seen, _ := engine.CacheLoadJSON[map[string]bool](ctx, securitySeenKey)
	if seen == nil {
		seen = make(map[string]bool, len(programs))
		for _, p := range programs {
			seen[p.URL] = true
		}
		engine.CacheStoreJSON(ctx, securitySeenKey, "", seen)
		slog.Info("security_monitor: initialized", slog.Int("count", len(seen)))
		return
	}

	var newPrograms []engine.SecurityProgram
	for _, p := range programs {
		if !seen[p.URL] {
			newPrograms = append(newPrograms, p)
			seen[p.URL] = true
		}
	}
	if len(newPrograms) == 0 {
		return
	}

	engine.CacheStoreJSON(ctx, securitySeenKey, "", seen)
	for _, p := range newPrograms {
		msg := formatSecurityNotification(p)
		if err := SendTelegramNotification(ctx, msg); err != nil {
			slog.Warn("security_monitor: notify failed", slog.Any("error", err), slog.String("url", p.URL))
		} else {
			slog.Info("security_monitor: notified", slog.String("name", p.Name))
		}
	}
}

func formatSecurityNotification(p engine.SecurityProgram) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("New Security Program [%s]\n", p.Platform))
	sb.WriteString(p.Name + "\n")
	if p.MaxBounty != "" {
		sb.WriteString("Max bounty: " + p.MaxBounty + "\n")
	}
	if len(p.Targets) > 0 {
		max := 3
		if len(p.Targets) < max {
			max = len(p.Targets)
		}
		sb.WriteString("Scope: " + strings.Join(p.Targets[:max], ", ") + "\n")
	}
	sb.WriteString(p.URL)
	return sb.String()
}
```

**Verify:** `go build github.com/anatolykoptev/go_job/...`

**Commit:** `git commit -m "feat: add security bounty program monitor with Telegram alerts"`

---

## Task 5: RemoteOK scraper

**Files:**
- Create: `internal/engine/jobs/remoteok.go`
- Create: `internal/engine/jobs/remoteok_test.go`

**Context:** RemoteOK API at `https://remoteok.com/api` returns JSON array. First element is metadata (skip it), rest are jobs. Fields: `slug`, `company`, `position`, `tags[]`, `salary_min`, `salary_max`, `url`, `date`, `location`.

**Test:**

```go
package jobs

import (
	"testing"
)

func TestParseRemoteOKResponse(t *testing.T) {
	t.Parallel()
	data := []byte(`[
		{"legal": "terms here"},
		{
			"slug": "test-job",
			"company": "Acme Corp",
			"position": "Senior Go Developer",
			"tags": ["golang", "devops"],
			"salary_min": 120000,
			"salary_max": 180000,
			"url": "https://remoteok.com/remote-jobs/12345",
			"date": "2026-03-09T12:00:00+00:00",
			"location": "Worldwide"
		}
	]`)
	jobs, err := parseRemoteOKResponse(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	j := jobs[0]
	if j.Title != "Senior Go Developer" {
		t.Errorf("title = %q, want %q", j.Title, "Senior Go Developer")
	}
	if j.Company != "Acme Corp" {
		t.Errorf("company = %q", j.Company)
	}
	if j.Source != "remoteok" {
		t.Errorf("source = %q", j.Source)
	}
	if j.SalaryMin != 120000 || j.SalaryMax != 180000 {
		t.Errorf("salary = %d-%d", j.SalaryMin, j.SalaryMax)
	}
}

func TestParseRemoteOKResponse_metadataOnly(t *testing.T) {
	t.Parallel()
	jobs, err := parseRemoteOKResponse([]byte(`[{"legal": "ok"}]`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("expected 0, got %d", len(jobs))
	}
}
```

**Implementation** (`remoteok.go`):

```go
package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/anatolykoptev/go_job/internal/engine"
)

const (
	remoteOKAPIURL         = "https://remoteok.com/api"
	remoteOKScrapeCacheKey = "remoteok_jobs"
)

type remoteOKJob struct {
	Slug      string   `json:"slug"`
	Company   string   `json:"company"`
	Position  string   `json:"position"`
	Tags      []string `json:"tags"`
	SalaryMin int      `json:"salary_min"`
	SalaryMax int      `json:"salary_max"`
	URL       string   `json:"url"`
	Date      string   `json:"date"`
	Location  string   `json:"location"`
}

// SearchRemoteOK fetches remote jobs from RemoteOK. Optionally filter by tag.
func SearchRemoteOK(ctx context.Context, tag string, limit int) ([]engine.FreelanceJob, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	cacheKey := remoteOKScrapeCacheKey
	if tag != "" {
		cacheKey += "_" + tag
	}
	if cached, ok := engine.CacheLoadJSON[[]engine.FreelanceJob](ctx, cacheKey); ok {
		slog.Debug("remoteok: using cached", slog.Int("count", len(cached)))
		if len(cached) > limit {
			cached = cached[:limit]
		}
		return cached, nil
	}

	jobs, err := fetchRemoteOK(ctx, tag)
	if err != nil {
		return nil, err
	}

	engine.CacheStoreJSON(ctx, cacheKey, "", jobs)
	if len(jobs) > limit {
		jobs = jobs[:limit]
	}
	slog.Debug("remoteok: fetch complete", slog.Int("count", len(jobs)))
	return jobs, nil
}

func fetchRemoteOK(ctx context.Context, tag string) ([]engine.FreelanceJob, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, engine.Cfg.FetchTimeout)
	defer cancel()

	url := remoteOKAPIURL
	if tag != "" {
		url += "?tag=" + tag
	}
	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", engine.UserAgentChrome)

	resp, err := engine.Cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("remoteok request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("remoteok returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 5*1024*1024))
	if err != nil {
		return nil, err
	}
	return parseRemoteOKResponse(body)
}

func parseRemoteOKResponse(data []byte) ([]engine.FreelanceJob, error) {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("remoteok parse: %w", err)
	}

	// First element is metadata/legal notice, skip it.
	jobs := make([]engine.FreelanceJob, 0, len(raw)-1)
	for i := 1; i < len(raw); i++ {
		var item remoteOKJob
		if err := json.Unmarshal(raw[i], &item); err != nil {
			continue
		}
		if item.Position == "" {
			continue
		}
		jobs = append(jobs, engine.FreelanceJob{
			Title:     item.Position,
			Company:   item.Company,
			URL:       item.URL,
			Tags:      item.Tags,
			SalaryMin: item.SalaryMin,
			SalaryMax: item.SalaryMax,
			Source:    "remoteok",
			Posted:    item.Date,
			Location:  item.Location,
		})
	}
	return jobs, nil
}
```

**Verify:** `go test github.com/anatolykoptev/go_job/internal/engine/jobs -run TestParseRemoteOK -v`

**Commit:** `git commit -m "feat: add RemoteOK job scraper"`

---

## Task 6: Himalayas scraper

**Files:**
- Create: `internal/engine/jobs/himalayas.go`
- Create: `internal/engine/jobs/himalayas_test.go`

**Context:** Himalayas API at `https://himalayas.app/jobs/api` returns `{"jobs": [...], "total": N}`. Job fields: `title`, `companyName`, `applicationUrl`, `categories[]`, `seniority[]`, `minSalary`, `maxSalary`, `timezoneRestrictions[]`, `excerpt`, `pubDate`.

**Test:**

```go
package jobs

import (
	"testing"
)

func TestParseHimalayasResponse(t *testing.T) {
	t.Parallel()
	data := []byte(`{
		"jobs": [{
			"title": "Backend Engineer",
			"companyName": "StartupCo",
			"applicationUrl": "https://himalayas.app/jobs/backend-123",
			"categories": ["Engineering"],
			"seniority": ["Senior"],
			"minSalary": 100000,
			"maxSalary": 150000,
			"pubDate": "2026-03-09",
			"excerpt": "Looking for a Go developer"
		}],
		"total": 1
	}`)
	jobs, err := parseHimalayasResponse(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	j := jobs[0]
	if j.Title != "Backend Engineer" {
		t.Errorf("title = %q", j.Title)
	}
	if j.Source != "himalayas" {
		t.Errorf("source = %q", j.Source)
	}
	if j.SalaryMin != 100000 {
		t.Errorf("salary_min = %d", j.SalaryMin)
	}
}

func TestParseHimalayasResponse_empty(t *testing.T) {
	t.Parallel()
	jobs, err := parseHimalayasResponse([]byte(`{"jobs": [], "total": 0}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("expected 0, got %d", len(jobs))
	}
}
```

**Implementation** (`himalayas.go`):

```go
package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/anatolykoptev/go_job/internal/engine"
)

const (
	himalayasAPIURL         = "https://himalayas.app/jobs/api"
	himalayasScrapeCacheKey = "himalayas_jobs"
)

type himalayasResponse struct {
	Jobs  []himalayasJob `json:"jobs"`
	Total int            `json:"total"`
}

type himalayasJob struct {
	Title          string   `json:"title"`
	CompanyName    string   `json:"companyName"`
	ApplicationURL string   `json:"applicationUrl"`
	Categories     []string `json:"categories"`
	Seniority      []string `json:"seniority"`
	MinSalary      int      `json:"minSalary"`
	MaxSalary      int      `json:"maxSalary"`
	PubDate        string   `json:"pubDate"`
	Excerpt        string   `json:"excerpt"`
}

// SearchHimalayas fetches remote jobs from Himalayas.app.
func SearchHimalayas(ctx context.Context, query string, limit int) ([]engine.FreelanceJob, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	cacheKey := himalayasScrapeCacheKey
	if query != "" {
		cacheKey += "_" + query
	}
	if cached, ok := engine.CacheLoadJSON[[]engine.FreelanceJob](ctx, cacheKey); ok {
		slog.Debug("himalayas: using cached", slog.Int("count", len(cached)))
		if len(cached) > limit {
			cached = cached[:limit]
		}
		return cached, nil
	}

	jobs, err := fetchHimalayas(ctx, query, limit)
	if err != nil {
		return nil, err
	}

	engine.CacheStoreJSON(ctx, cacheKey, "", jobs)
	slog.Debug("himalayas: fetch complete", slog.Int("count", len(jobs)))
	return jobs, nil
}

func fetchHimalayas(ctx context.Context, query string, limit int) ([]engine.FreelanceJob, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, engine.Cfg.FetchTimeout)
	defer cancel()

	u := himalayasAPIURL + "?limit=" + fmt.Sprintf("%d", limit)
	if query != "" {
		u += "&q=" + url.QueryEscape(query)
	}
	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", engine.UserAgentChrome)

	resp, err := engine.Cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("himalayas request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("himalayas returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 5*1024*1024))
	if err != nil {
		return nil, err
	}
	return parseHimalayasResponse(body)
}

func parseHimalayasResponse(data []byte) ([]engine.FreelanceJob, error) {
	var resp himalayasResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("himalayas parse: %w", err)
	}

	jobs := make([]engine.FreelanceJob, 0, len(resp.Jobs))
	for _, item := range resp.Jobs {
		if item.Title == "" {
			continue
		}
		tags := append(item.Categories, item.Seniority...)
		jobs = append(jobs, engine.FreelanceJob{
			Title:     item.Title,
			Company:   item.CompanyName,
			URL:       item.ApplicationURL,
			Tags:      tags,
			SalaryMin: item.MinSalary,
			SalaryMax: item.MaxSalary,
			Source:    "himalayas",
			Posted:    item.PubDate,
		})
	}
	return jobs, nil
}
```

**Verify:** `go test github.com/anatolykoptev/go_job/internal/engine/jobs -run TestParseHimalayas -v`

**Commit:** `git commit -m "feat: add Himalayas.app job scraper"`

---

## Task 7: Freelance job monitor

**Files:**
- Create: `internal/engine/jobs/freelance_monitor.go`

**Context:** Follow `bounty_monitor.go` pattern. Poll RemoteOK (tags: golang, devops, security) and Himalayas (query: golang). Use job URL as unique key. 30-min interval. Telegram notification with title, company, salary, tags, URL.

**Implementation:**

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

const freelanceSeenKey = "freelance_seen_ids"

// StartFreelanceMonitor launches background monitoring for new freelance/remote jobs.
func StartFreelanceMonitor(ctx context.Context) {
	interval := 30 * time.Minute
	if engine.Cfg.VaelorNotifyURL == "" {
		slog.Info("freelance_monitor: disabled (VAELOR_NOTIFY_URL not set)")
		return
	}
	slog.Info("freelance_monitor: starting", slog.Duration("interval", interval))

	time.AfterFunc(60*time.Second, func() { checkNewFreelanceJobs(ctx) })

	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				slog.Info("freelance_monitor: stopped")
				return
			case <-ticker.C:
				checkNewFreelanceJobs(ctx)
			}
		}
	}()
}

func checkNewFreelanceJobs(ctx context.Context) {
	var allJobs []engine.FreelanceJob

	for _, tag := range []string{"golang", "devops", "security"} {
		jobs, err := SearchRemoteOK(ctx, tag, 30)
		if err != nil {
			slog.Warn("freelance_monitor: remoteok failed", slog.String("tag", tag), slog.Any("error", err))
			continue
		}
		allJobs = append(allJobs, jobs...)
	}

	himJobs, himErr := SearchHimalayas(ctx, "golang", 30)
	if himErr != nil {
		slog.Warn("freelance_monitor: himalayas failed", slog.Any("error", himErr))
	}
	allJobs = append(allJobs, himJobs...)

	if len(allJobs) == 0 {
		return
	}

	seen, _ := engine.CacheLoadJSON[map[string]bool](ctx, freelanceSeenKey)
	if seen == nil {
		seen = make(map[string]bool, len(allJobs))
		for _, j := range allJobs {
			seen[j.URL] = true
		}
		engine.CacheStoreJSON(ctx, freelanceSeenKey, "", seen)
		slog.Info("freelance_monitor: initialized", slog.Int("count", len(seen)))
		return
	}

	var newJobs []engine.FreelanceJob
	for _, j := range allJobs {
		if !seen[j.URL] {
			newJobs = append(newJobs, j)
			seen[j.URL] = true
		}
	}
	if len(newJobs) == 0 {
		return
	}

	engine.CacheStoreJSON(ctx, freelanceSeenKey, "", seen)
	for _, j := range newJobs {
		msg := formatFreelanceNotification(j)
		if err := SendTelegramNotification(ctx, msg); err != nil {
			slog.Warn("freelance_monitor: notify failed", slog.Any("error", err), slog.String("url", j.URL))
		} else {
			slog.Info("freelance_monitor: notified", slog.String("title", j.Title))
		}
	}
}

func formatFreelanceNotification(j engine.FreelanceJob) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("New Job [%s]\n", j.Source))
	sb.WriteString(j.Title + "\n")
	if j.Company != "" {
		sb.WriteString(j.Company + "\n")
	}
	if j.SalaryMin > 0 || j.SalaryMax > 0 {
		sb.WriteString(fmt.Sprintf("Salary: $%dk-$%dk\n", j.SalaryMin/1000, j.SalaryMax/1000))
	}
	if len(j.Tags) > 0 {
		sb.WriteString("Tags: " + strings.Join(j.Tags, ", ") + "\n")
	}
	sb.WriteString(j.URL)
	return sb.String()
}
```

**Verify:** `go build github.com/anatolykoptev/go_job/...`

**Commit:** `git commit -m "feat: add freelance job monitor with Telegram alerts"`

---

## Task 8: MCP tool — security_bounty_search

**Files:**
- Create: `internal/jobserver/tool_security_bounty.go`
- Modify: `internal/jobserver/register.go`

**Implementation** (`tool_security_bounty.go`):

```go
package jobserver

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type securitySearchInput struct {
	Platform string `json:"platform" jsonschema:"Filter by platform: hackerone, bugcrowd, intigriti, yeswehack, immunefi. Empty returns all."`
	Query    string `json:"query" jsonschema:"Search keyword to filter programs by name or scope (e.g. 'crypto', 'api'). Empty returns all."`
}

func registerSecurityBountySearch(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "security_bounty_search",
		Description: "Search for security bug bounty programs across HackerOne, Bugcrowd, Intigriti, YesWeHack, and Immunefi. Returns program name, platform, max bounty, and in-scope targets.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input securitySearchInput) (*mcp.CallToolResult, engine.SmartSearchOutput, error) {
		btdPrograms, err := jobs.SearchSecurityPrograms(ctx, 500)
		if err != nil {
			return nil, engine.SmartSearchOutput{}, err
		}

		immPrograms, immErr := jobs.SearchImmunefi(ctx, 500)
		if immErr != nil && err != nil {
			return nil, engine.SmartSearchOutput{}, errors.New("all security sources failed")
		}
		all := append(btdPrograms, immPrograms...)

		filtered := filterSecurityPrograms(all, input)

		jsonBytes, _ := json.Marshal(filtered)
		return nil, engine.SmartSearchOutput{
			Query:   input.Query,
			Answer:  string(jsonBytes),
			Sources: []engine.SourceItem{},
		}, nil
	})
}

func filterSecurityPrograms(programs []engine.SecurityProgram, input securitySearchInput) []engine.SecurityProgram {
	if input.Platform == "" && input.Query == "" {
		if len(programs) > 100 {
			return programs[:100]
		}
		return programs
	}

	var filtered []engine.SecurityProgram
	for _, p := range programs {
		if input.Platform != "" && p.Platform != input.Platform {
			continue
		}
		if input.Query != "" && !containsIgnoreCase(p.Name, input.Query) && !targetsContain(p.Targets, input.Query) {
			continue
		}
		filtered = append(filtered, p)
	}
	if len(filtered) > 100 {
		filtered = filtered[:100]
	}
	return filtered
}

func containsIgnoreCase(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		findIgnoreCase(s, substr))
}

func findIgnoreCase(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		match := true
		for j := range len(sub) {
			cs, csub := s[i+j], sub[j]
			if cs >= 'A' && cs <= 'Z' { cs += 32 }
			if csub >= 'A' && csub <= 'Z' { csub += 32 }
			if cs != csub { match = false; break }
		}
		if match { return true }
	}
	return false
}

func targetsContain(targets []string, query string) bool {
	for _, t := range targets {
		if findIgnoreCase(t, query) {
			return true
		}
	}
	return false
}
```

**Modify `register.go`** — add after the Bounties section:

```go
	// Security Bug Bounties
	registerSecurityBountySearch(server)
```

**Verify:** `go build github.com/anatolykoptev/go_job/...`

**Commit:** `git commit -m "feat: add security_bounty_search MCP tool"`

---

## Task 9: Register monitors in main.go and deploy

**Files:**
- Modify: `main.go:158` (add StartSecurityMonitor and StartFreelanceMonitor calls)

**Implementation:**

After `jobs.StartBountyMonitor(context.Background())` add:

```go
	jobs.StartSecurityMonitor(context.Background())
	jobs.StartFreelanceMonitor(context.Background())
```

**Verify all tests pass:**
```bash
go test github.com/anatolykoptev/go_job/internal/engine/jobs -v
```

**Verify build:**
```bash
go build github.com/anatolykoptev/go_job/...
```

**Commit:** `git commit -m "feat: register security and freelance monitors in main"`

**Deploy:**
```bash
cd ~/deploy/krolik-server
docker compose build --no-cache go-job
docker compose up -d --no-deps --force-recreate go-job
```

**Verify logs:**
```bash
docker logs go-job --tail 20
```

Expected: `security_monitor: starting`, `freelance_monitor: starting`, `tools registered count=28`

---
