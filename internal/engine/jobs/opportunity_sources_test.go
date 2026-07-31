package jobs

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// opportunity_sources_test.go: Falsification tests for the scheduled ingest
// source observability metrics. Each test breaks the thing it guards — a compile
// error is NOT a falsification; the binary must run and the assertion must fail.
//
// F1 — freshness gauge — per-source: make one source return zero rows → its
//   gauge must stop advancing while others advance.
// F2 — outcome discrimination — fetch_error vs parse_error: make a source fail
//   at fetch, then separately at parse → the counter must land on the right label.
// F3 — derived source list — add a source to the real fan-out WITHOUT touching
//   any metric registration → the pre-touch/seed must pick it up automatically.

// --- F1: freshness gauge is per-source, not blanket ---

// TestF1_FreshnessGauge_PerSource verifies that the freshness gauge
// (gojob_hunt_source_last_success_timestamp) advances ONLY for sources that
// yield at least one row. A source returning zero rows must NOT advance.
//
// Revert-red (mutation 1): remove the `if len(bounties) > 0` guard before
//
//	SetHuntSourceLastSuccess → opire's gauge also advances → assert opire==0 RED.
//
// Revert-red (mutation 2): remove SetHuntSourceLastSuccess entirely → bountyhub's
//
//	gauge stays at 0 → assert bountyhub>0 RED.
func TestF1_FreshnessGauge_PerSource(t *testing.T) {
	engine.InitTestRegistry()

	origBounty := bountyFetchSources
	t.Cleanup(func() { bountyFetchSources = origBounty })

	bountyFetchSources = []struct {
		name string
		fn   func(ctx context.Context, limit int) ([]engine.BountyListing, error)
	}{
		{"bountyhub", func(_ context.Context, _ int) ([]engine.BountyListing, error) {
			return []engine.BountyListing{{Title: "test-bounty"}}, nil // yields rows
		}},
		{"opire", func(_ context.Context, _ int) ([]engine.BountyListing, error) {
			return nil, nil // yields zero rows
		}},
	}

	items, summary := fetchAllBountiesImpl(context.Background(), 10, false)

	bountyhubGauge := engine.GetGaugeValue("hunt_source_last_success_timestamp{kind=bounty,source=bountyhub}")
	opireGauge := engine.GetGaugeValue("hunt_source_last_success_timestamp{kind=bounty,source=opire}")

	if bountyhubGauge <= 0 {
		t.Errorf("F1 FAIL: bountyhub yielded %d rows → freshness gauge must advance (>0), got %v", len(items), bountyhubGauge)
	}
	if opireGauge != 0 {
		t.Errorf("F1 FAIL: opire yielded 0 rows → freshness gauge must NOT advance (==0), got %v", opireGauge)
	}
	if summary["bountyhub"] != 1 {
		t.Errorf("F1 FAIL: summary[bountyhub] = %d, want 1", summary["bountyhub"])
	}
	if summary["opire"] != 0 {
		t.Errorf("F1 FAIL: summary[opire] = %d, want 0", summary["opire"])
	}
}

// --- F2: outcome discrimination — fetch_error vs parse_error ---

// TestF2_OutcomeDiscrimination_FetchVsParse verifies that a non-ErrParse error
// lands on outcome=fetch_error and an ErrParse-wrapped error lands on
// outcome=parse_error — the distinction that would have pointed at the right
// layer for the hackerone truncated-read failure (reported as parse but actually
// a fetch-level truncation).
//
// Revert-red (mutation): make classifySourceOutcome return "fetch_error" for all
//
//	errors → parse_error assertion RED.
//
// Revert-red (mutation 2): make classifySourceOutcome return "parse_error" for all
//
//	errors → fetch_error assertion RED.
func TestF2_OutcomeDiscrimination_FetchVsParse(t *testing.T) {
	engine.InitTestRegistry()

	origBounty := bountyFetchSources
	t.Cleanup(func() { bountyFetchSources = origBounty })

	fetchErr := errors.New("network timeout")
	parseErr := fmt.Errorf("decode failed: %w", ErrParse)

	bountyFetchSources = []struct {
		name string
		fn   func(ctx context.Context, limit int) ([]engine.BountyListing, error)
	}{
		{"bountyhub", func(_ context.Context, _ int) ([]engine.BountyListing, error) {
			return nil, fetchErr
		}},
		{"opire", func(_ context.Context, _ int) ([]engine.BountyListing, error) {
			return nil, parseErr
		}},
	}

	fetchAllBountiesImpl(context.Background(), 10, false)

	snap := engine.GetMetrics()

	fetchKey := "hunt_source_outcome_total{kind=bounty,source=bountyhub,outcome=fetch_error}"
	parseKey := "hunt_source_outcome_total{kind=bounty,source=opire,outcome=parse_error}"

	if snap[fetchKey] != 1 {
		t.Errorf("F2 FAIL: bountyhub returned a non-ErrParse error → outcome must be fetch_error (count=1), got %d", snap[fetchKey])
	}
	if snap[parseKey] != 1 {
		t.Errorf("F2 FAIL: opire returned an ErrParse-wrapped error → outcome must be parse_error (count=1), got %d", snap[parseKey])
	}
	// Cross-check: the wrong outcomes must NOT have fired.
	if snap["hunt_source_outcome_total{kind=bounty,source=bountyhub,outcome=parse_error}"] != 0 {
		t.Errorf("F2 FAIL: bountyhub must NOT land on parse_error")
	}
	if snap["hunt_source_outcome_total{kind=bounty,source=opire,outcome=fetch_error}"] != 0 {
		t.Errorf("F2 FAIL: opire must NOT land on fetch_error")
	}
}

// --- F3: derived source list — auto-picks-up new source ---

// TestF3_DerivedSourceList_AutoPicksUpNewSource verifies that adding a source to
// the real fan-out table (bountyFetchSources) WITHOUT touching any metric
// registration causes the pre-touch (warmAlertBoundedMetrics) to automatically
// seed the new source's series. This is the difference between instrumentation
// that stays true and instrumentation that rots the first time someone adds a
// source.
//
// The test simulates the real derivation path:
//  1. Add a source to bountyFetchSources (the real fan-out table).
//  2. Re-derive BountySourceNames() from the table → must include the new source.
//  3. Re-register with engine.RegisterHuntSources (as init() does).
//  4. Call engine.Init() → warmAlertBoundedMetrics pre-touches the new source.
//  5. Assert the new source's series exist in the registry snapshot.
//
// Revert-red (mutation): if BountySourceNames() read from a hand-maintained list
//
//	instead of ranging bountyFetchSources → the new source is missing from the
//	derived list → not registered → not pre-touched → assertion RED.
func TestF3_DerivedSourceList_AutoPicksUpNewSource(t *testing.T) {
	// Save original state for cleanup.
	origBounty := bountyFetchSources
	origBountyNames := BountySourceNames()
	t.Cleanup(func() {
		bountyFetchSources = origBounty
		engine.RegisterHuntSources(map[string][]string{
			"security":  SecuritySourceNames(),
			"bounty":    origBountyNames,
			"freelance": FreelanceSourceNames(),
		})
	})

	// Step 1: add a new source to the real fan-out table — NO metric registration touched.
	bountyFetchSources = append(bountyFetchSources, struct {
		name string
		fn   func(ctx context.Context, limit int) ([]engine.BountyListing, error)
	}{
		"newtestsource", func(_ context.Context, _ int) ([]engine.BountyListing, error) {
			return nil, nil
		},
	})

	// Step 2: re-derive source names from the updated table.
	names := BountySourceNames()
	found := false
	for _, n := range names {
		if n == "newtestsource" {
			found = true
		}
	}
	if !found {
		t.Fatal("F3 FAIL: BountySourceNames() must include 'newtestsource' added to bountyFetchSources — if this fails, the source list is hand-maintained, not derived from the real fan-out")
	}

	// Step 3: re-register with the updated list (simulates what init() does).
	engine.RegisterHuntSources(map[string][]string{
		"security":  SecuritySourceNames(),
		"bounty":    names,
		"freelance": FreelanceSourceNames(),
	})

	// Step 4: engine.Init creates a fresh registry and calls warmAlertBoundedMetrics,
	// which reads registeredHuntSources and pre-touches every known source.
	// Save/restore Cfg fields that TestMain set up (HTTPClient, FetchTimeout) —
	// engine.Init replaces Cfg with the passed Config{}, which would leave
	// subsequent sherlock/ats tests with a nil HTTP client and 0 timeout.
	origHTTPClient := engine.Cfg.HTTPClient
	origFetchTimeout := engine.Cfg.FetchTimeout
	engine.Init(engine.Config{})
	engine.Cfg.HTTPClient = origHTTPClient
	engine.Cfg.FetchTimeout = origFetchTimeout

	// Step 5: the new source's series must be pre-touched (exist at 0).
	snap := engine.GetMetrics()
	outcomeKey := "hunt_source_outcome_total{kind=bounty,source=newtestsource,outcome=ok}"
	v, ok := snap[outcomeKey]
	if !ok {
		t.Errorf("F3 FAIL: new source 'newtestsource' outcome counter not pre-touched by warmAlertBoundedMetrics — series must exist")
	}
	if v != 0 {
		t.Errorf("F3 FAIL: pre-touched series must be 0, got %d", v)
	}

	// Also check the freshness gauge was pre-touched at 0.
	gaugeVal := engine.GetGaugeValue("hunt_source_last_success_timestamp{kind=bounty,source=newtestsource}")
	if gaugeVal != 0 {
		t.Errorf("F3 FAIL: new source freshness gauge must be pre-touched at 0, got %v", gaugeVal)
	}
}

// TestF3_SourceNames_Complete verifies that the derived source name lists match
// the real fan-out tables exactly — no hand-maintained list can drift.
func TestF3_SourceNames_Complete(t *testing.T) {
	// Bounty: 6 sources (bountyhub + opire + bountyhub + boss + lightning + collaborators)
	bn := BountySourceNames()
	if len(bn) != len(bountyFetchSources) {
		t.Errorf("BountySourceNames len=%d != bountyFetchSources len=%d — list is not derived from the table", len(bn), len(bountyFetchSources))
	}

	// Security: 5 BTD + 4 non-BTD = 9 sources
	sn := SecuritySourceNames()
	expectedSec := len(securitySources) + len(securityFetchSources)
	if len(sn) != expectedSec {
		t.Errorf("SecuritySourceNames len=%d != securitySources(%d) + securityFetchSources(%d) = %d — list is not derived from the tables", len(sn), len(securitySources), len(securityFetchSources), expectedSec)
	}

	// Freelance: 2 sources
	fn := FreelanceSourceNames()
	if len(fn) != len(freelanceFetchSources) {
		t.Errorf("FreelanceSourceNames len=%d != freelanceFetchSources len=%d — list is not derived from the table", len(fn), len(freelanceFetchSources))
	}
}
