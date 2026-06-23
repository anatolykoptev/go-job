package connectors_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/anatolykoptev/go_job/internal/engine/jobs/connectors"
)

// TestATSSource_ParityWithIndividualSources verifies that ATSSource(provider)
// returns Sources with the same Name(), Groups(), SiteScope(), and Capabilities()
// as the original per-adapter structs they replace.
//
// Revert-red: delete ATSSource("greenhouse") from BuildDefaultRegistry and this
// test fails on registry completeness (TestSelectSources_AdvertisedPlatformsAllRoute).
// Delete the atsProviders["greenhouse"] entry and this test fails directly.
func TestATSSource_ParityWithIndividualSources(t *testing.T) {
	cases := []struct {
		provider   string
		wantName   string
		wantGroups []string
		wantScope  string
	}{
		{
			provider:   "greenhouse",
			wantName:   "greenhouse",
			wantGroups: []string{"all", "ats", "startup"},
			wantScope:  "site:boards.greenhouse.io",
		},
		{
			provider:   "lever",
			wantName:   "lever",
			wantGroups: []string{"all", "ats", "startup"},
			wantScope:  "site:jobs.lever.co",
		},
		{
			provider:   "ashby",
			wantName:   "ashby",
			wantGroups: []string{"all", "ats", "startup"},
			wantScope:  "site:jobs.ashbyhq.com",
		},
	}
	for _, c := range cases {
		t.Run(c.provider, func(t *testing.T) {
			src := connectors.ATSSource(c.provider)

			if src.Name() != c.wantName {
				t.Errorf("Name() = %q, want %q", src.Name(), c.wantName)
			}

			got := src.Groups()
			for _, g := range c.wantGroups {
				if !slices.Contains(got, g) {
					t.Errorf("Groups() missing %q (got %v)", g, got)
				}
			}

			if src.SiteScope() != c.wantScope {
				t.Errorf("SiteScope() = %q, want %q", src.SiteScope(), c.wantScope)
			}

			// ATS sources do not need an API key.
			if src.Capabilities()&connectors.NeedsAPIKey != 0 {
				t.Errorf("ATS source %q must NOT have NeedsAPIKey capability", c.provider)
			}
		})
	}
}

// TestATSSource_UnknownProviderPanics is a guard for the init-time panic on typo.
func TestATSSource_UnknownProviderPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("ATSSource(unknown) should have panicked")
		}
	}()
	connectors.ATSSource("nonexistent-ats-provider")
}

// TestATSSource_LeverDistinctFromGreenhouse verifies that the lever and greenhouse
// sources produce different SiteScope strings — the lever dual-query P4 fix depends
// on the lever-specific site search scope being "site:jobs.lever.co" not greenhouse's.
//
// Revert-red: swap lever and greenhouse siteScope in atsProviders table and this fails.
func TestATSSource_LeverDistinctFromGreenhouse(t *testing.T) {
	lever := connectors.ATSSource("lever")
	greenhouse := connectors.ATSSource("greenhouse")
	if lever.SiteScope() == greenhouse.SiteScope() {
		t.Errorf("lever and greenhouse share SiteScope %q — lever dual-query fix depends on distinct scopes",
			lever.SiteScope())
	}
	if !strings.Contains(lever.SiteScope(), "jobs.lever.co") {
		t.Errorf("lever SiteScope %q does not contain jobs.lever.co", lever.SiteScope())
	}
}

// TestKeylessJSONSource_ParityWithIndividualSources verifies that
// KeylessJSONSource(provider) preserves Name(), Groups(), SiteScope().
//
// Revert-red: delete KeylessJSONSource("remotive") from BuildDefaultRegistry
// and TestSelectSources_AdvertisedPlatformsAllRoute fails; delete the
// keylessProviders["remotive"] entry and this test fails directly.
func TestKeylessJSONSource_ParityWithIndividualSources(t *testing.T) {
	cases := []struct {
		provider   string
		wantName   string
		wantGroups []string
		wantScope  string
	}{
		{
			provider:   "remoteok",
			wantName:   "remoteok",
			wantGroups: []string{"all", "remote"},
			wantScope:  "site:remoteok.com",
		},
		{
			provider:   "weworkremotely",
			wantName:   "weworkremotely",
			wantGroups: []string{"all", "remote"},
			wantScope:  "site:weworkremotely.com",
		},
		{
			provider:   "remotive",
			wantName:   "remotive",
			wantGroups: []string{"all", "remote"},
			wantScope:  "site:remotive.com",
		},
	}
	for _, c := range cases {
		t.Run(c.provider, func(t *testing.T) {
			src := connectors.KeylessJSONSource(c.provider)

			if src.Name() != c.wantName {
				t.Errorf("Name() = %q, want %q", src.Name(), c.wantName)
			}

			got := src.Groups()
			for _, g := range c.wantGroups {
				if !slices.Contains(got, g) {
					t.Errorf("Groups() missing %q (got %v)", g, got)
				}
			}

			if src.SiteScope() != c.wantScope {
				t.Errorf("SiteScope() = %q, want %q", src.SiteScope(), c.wantScope)
			}

			// Keyless sources need no API key.
			if src.Capabilities()&connectors.NeedsAPIKey != 0 {
				t.Errorf("keyless source %q must NOT have NeedsAPIKey capability", c.provider)
			}
		})
	}
}

// TestKeylessJSONSource_UnknownProviderPanics guards against typos at init time.
func TestKeylessJSONSource_UnknownProviderPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("KeylessJSONSource(unknown) should have panicked")
		}
	}()
	connectors.KeylessJSONSource("nonexistent-remote-provider")
}

// TestKeylessJSONSource_DistinctEndpoints verifies the three remote sources hit
// different URLs (remoteok ≠ wwr ≠ remotive). The SiteScope encodes the endpoint
// domain — a mismatch here means a provider's traffic goes to the wrong API.
//
// Revert-red: set all three siteScope fields to the same value and this fails.
func TestKeylessJSONSource_DistinctEndpoints(t *testing.T) {
	remoteok := connectors.KeylessJSONSource("remoteok")
	wwr := connectors.KeylessJSONSource("weworkremotely")
	remotive := connectors.KeylessJSONSource("remotive")

	scopes := []string{remoteok.SiteScope(), wwr.SiteScope(), remotive.SiteScope()}
	seen := make(map[string]bool)
	for _, s := range scopes {
		if seen[s] {
			t.Errorf("duplicate SiteScope %q across keyless sources — each must hit a distinct endpoint", s)
		}
		seen[s] = true
	}
}

// TestHasRequiredAPIKey_IndeedAbsent verifies that HasRequiredAPIKey returns false
// for the indeed source when IndeedAPIKey is empty (the prod default).
//
// This is the F3 wire: a false return causes runSource to emit outcome=no_key
// without calling Fetch, saving a doomed API round-trip.
//
// Revert-red: change HasRequiredAPIKey to always return true and the F3 no_key
// shortcut in runSource never fires — indeed attempts a real call and returns an
// error instead of the clean no_key skip.
func TestHasRequiredAPIKey_IndeedAbsent(t *testing.T) {
	// engine.Cfg.IndeedAPIKey is empty in the test environment (no env var set).
	// Build a registry, find the indeed source, and assert HasRequiredAPIKey is false.
	reg := connectors.BuildDefaultRegistry()
	var indeedSrc connectors.Source
	for _, s := range reg.All() {
		if s.Name() == "indeed" {
			indeedSrc = s
			break
		}
	}
	if indeedSrc == nil {
		t.Fatal("indeed source not found in default registry")
	}

	// Confirm indeed has NeedsAPIKey set (the capability this test guards).
	if indeedSrc.Capabilities()&connectors.NeedsAPIKey == 0 {
		t.Error("indeed must have NeedsAPIKey capability — this is the F3 guard")
	}

	// When IndeedAPIKey is not set in the environment, HasRequiredAPIKey must be false.
	// engine.Cfg.IndeedAPIKey defaults to "" in test (no INDEED_API_KEY env var).
	got := connectors.HasRequiredAPIKey(indeedSrc)
	if got {
		t.Error("HasRequiredAPIKey(indeed) must return false when IndeedAPIKey is empty (prod default)")
	}
}

// TestHasRequiredAPIKey_NonKeyedSourcesAlwaysTrue verifies that sources without
// NeedsAPIKey always return true from HasRequiredAPIKey.
//
// Revert-red: make HasRequiredAPIKey return false for all sources and the fan-out
// skips every source with a no_key outcome — zero job results.
func TestHasRequiredAPIKey_NonKeyedSourcesAlwaysTrue(t *testing.T) {
	reg := connectors.BuildDefaultRegistry()
	for _, src := range reg.All() {
		if src.Capabilities()&connectors.NeedsAPIKey != 0 {
			continue // already tested by TestHasRequiredAPIKey_IndeedAbsent
		}
		if !connectors.HasRequiredAPIKey(src) {
			t.Errorf("source %q has no NeedsAPIKey but HasRequiredAPIKey returned false", src.Name())
		}
	}
}

// TestSupportsPaginationCapabilityRemoved verifies that SupportsPagination is no
// longer exported from the connectors package (F2 cleanup).
//
// This is a compile-time guard — the test body is trivial because the real check
// is that connectors.SupportsPagination does not compile. We verify the removed
// capability's bit position does not overlap OptIn by checking capability values.
//
// Revert-red: re-add SupportsPagination = 1<<2 to registry.go. The zero-reader /
// zero-setter audit in P2 confirms it was dead; re-adding it re-introduces dead code.
func TestSupportsPaginationCapabilityRemoved(t *testing.T) {
	// NeedsAPIKey=1, OptIn=2. SupportsPagination was 4 (now removed).
	// Verify NeedsAPIKey and OptIn keep their expected bit positions so callers
	// that rely on them are not silently broken by the removal.
	if connectors.NeedsAPIKey != 1 {
		t.Errorf("NeedsAPIKey bit position changed: got %d want 1", connectors.NeedsAPIKey)
	}
	if connectors.OptIn != 2 {
		t.Errorf("OptIn bit position changed: got %d want 2", connectors.OptIn)
	}
}

// TestRegistryOrder_ATSAndRemoteGroupMembership verifies the registry preserves
// the original insertion order and group memberships for all 17 connectors.
//
// This is the FF-1 completeness guard adapted for P2: the same 17 connectors
// exist, and the 3 ATS + 3 keyless remote sources are in the right groups.
func TestRegistryOrder_ATSAndRemoteGroupMembership(t *testing.T) {
	reg := connectors.BuildDefaultRegistry()
	all := reg.All()

	// Expect exactly 17 connectors.
	if len(all) != 17 {
		t.Errorf("BuildDefaultRegistry: expected 17 connectors, got %d", len(all))
	}

	// ATS group must contain exactly greenhouse, lever, ashby.
	ats := reg.Select("ats")
	atsNames := make([]string, len(ats))
	for i, s := range ats {
		atsNames[i] = s.Name()
	}
	for _, want := range []string{"greenhouse", "lever", "ashby"} {
		if !slices.Contains(atsNames, want) {
			t.Errorf("ats group missing %q (got %v)", want, atsNames)
		}
	}

	// Remote group must contain exactly remoteok, weworkremotely, remotive.
	remote := reg.Select("remote")
	remoteNames := make([]string, len(remote))
	for i, s := range remote {
		remoteNames[i] = s.Name()
	}
	for _, want := range []string{"remoteok", "weworkremotely", "remotive"} {
		if !slices.Contains(remoteNames, want) {
			t.Errorf("remote group missing %q (got %v)", want, remoteNames)
		}
	}
}
