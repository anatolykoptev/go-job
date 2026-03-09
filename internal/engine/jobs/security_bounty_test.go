package jobs

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sampleHackerOneData = `[
  {
    "name": "Acme Corp",
    "handle": "acme",
    "url": "https://hackerone.com/acme",
    "offers_bounties": true,
    "managed_program": true,
    "targets": {
      "in_scope": [
        {"asset_identifier": "*.acme.com", "asset_type": "WILDCARD", "eligible_for_bounty": true},
        {"asset_identifier": "api.acme.com", "asset_type": "URL", "eligible_for_bounty": true}
      ]
    }
  },
  {
    "name": "VDP Only",
    "handle": "vdp",
    "url": "https://hackerone.com/vdp",
    "offers_bounties": false,
    "managed_program": false,
    "targets": {
      "in_scope": [
        {"asset_identifier": "vdp.example.com", "asset_type": "URL", "eligible_for_bounty": false}
      ]
    }
  }
]`

const sampleBugcrowdData = `[
  {
    "name": "CoinDesk Data",
    "url": "https://bugcrowd.com/engagements/coindesk",
    "managed_by_bugcrowd": true,
    "max_payout": 7500,
    "targets": {
      "in_scope": [
        {"type": "api", "target": "http://data-api.coindesk.com/"},
        {"type": "website", "target": "https://www.coindesk.com/"}
      ]
    }
  },
  {
    "name": "No Payout Corp",
    "url": "https://bugcrowd.com/engagements/nopay",
    "managed_by_bugcrowd": false,
    "max_payout": 0,
    "targets": {"in_scope": []}
  }
]`

const sampleIntigritiData = `[
  {
    "name": "AMD Bug Bounty",
    "handle": "amd",
    "url": "https://www.intigriti.com/programs/amd/amd/detail",
    "min_bounty": {"value": 500, "currency": "USD"},
    "max_bounty": {"value": 30000, "currency": "USD"},
    "targets": {
      "in_scope": [
        {"type": "other", "endpoint": "Hardware"},
        {"type": "url", "endpoint": "www.amd.com"}
      ]
    }
  }
]`

const sampleYesWeHackData = `[
  {
    "id": "outscale",
    "name": "3DS OUTSCALE",
    "public": true,
    "disabled": false,
    "managed": null,
    "min_bounty": 50,
    "max_bounty": 5000,
    "targets": {
      "in_scope": [
        {"target": "https://cockpit-eu-west-2.outscale.com/", "type": "web-application"},
        {"target": "https://api.outscale.com", "type": "api"}
      ]
    }
  },
  {
    "id": "disabled-prog",
    "name": "Disabled Program",
    "public": true,
    "disabled": true,
    "managed": null,
    "min_bounty": 0,
    "max_bounty": 0,
    "targets": {"in_scope": []}
  }
]`

func TestParseHackerOnePrograms(t *testing.T) {
	t.Parallel()
	programs, err := parseHackerOneData([]byte(sampleHackerOneData))
	require.NoError(t, err)
	require.Len(t, programs, 2)

	p := programs[0]
	assert.Equal(t, "Acme Corp", p.Name)
	assert.Equal(t, "hackerone", p.Platform)
	assert.Equal(t, "https://hackerone.com/acme", p.URL)
	assert.Equal(t, "bug_bounty", p.Type)
	assert.True(t, p.Managed)
	assert.Contains(t, p.Targets, "*.acme.com")
	assert.Contains(t, p.Targets, "api.acme.com")

	vdp := programs[1]
	assert.Equal(t, "vdp", vdp.Type)
	assert.False(t, vdp.Managed)
}

func TestParseBugcrowdPrograms(t *testing.T) {
	t.Parallel()
	programs, err := parseBugcrowdData([]byte(sampleBugcrowdData))
	require.NoError(t, err)
	require.Len(t, programs, 2)

	p := programs[0]
	assert.Equal(t, "CoinDesk Data", p.Name)
	assert.Equal(t, "bugcrowd", p.Platform)
	assert.Equal(t, "https://bugcrowd.com/engagements/coindesk", p.URL)
	assert.Equal(t, "$7,500", p.MaxBounty)
	assert.True(t, p.Managed)
	assert.Equal(t, "bug_bounty", p.Type)
	assert.Contains(t, p.Targets, "http://data-api.coindesk.com/")

	nopay := programs[1]
	assert.Equal(t, "vdp", nopay.Type)
	assert.False(t, nopay.Managed)
}

func TestParseIntigritiPrograms(t *testing.T) {
	t.Parallel()
	programs, err := parseIntigritiData([]byte(sampleIntigritiData))
	require.NoError(t, err)
	require.Len(t, programs, 1)

	p := programs[0]
	assert.Equal(t, "AMD Bug Bounty", p.Name)
	assert.Equal(t, "intigriti", p.Platform)
	assert.Equal(t, "$30,000", p.MaxBounty)
	assert.Equal(t, "$500", p.MinBounty)
	assert.Contains(t, p.Targets, "Hardware")
	assert.Contains(t, p.Targets, "www.amd.com")
}

func TestParseYesWeHackPrograms(t *testing.T) {
	t.Parallel()
	programs, err := parseYesWeHackData([]byte(sampleYesWeHackData))
	require.NoError(t, err)
	require.Len(t, programs, 1) // disabled one is skipped

	p := programs[0]
	assert.Equal(t, "3DS OUTSCALE", p.Name)
	assert.Equal(t, "yeswehack", p.Platform)
	assert.Equal(t, "https://yeswehack.com/programs/outscale", p.URL)
	assert.Equal(t, "$5,000", p.MaxBounty)
	assert.Equal(t, "$50", p.MinBounty)
	assert.Contains(t, p.Targets, "https://cockpit-eu-west-2.outscale.com/")
}

func TestParseSecurityEmpty(t *testing.T) {
	t.Parallel()

	h1, err := parseHackerOneData([]byte(`[]`))
	require.NoError(t, err)
	assert.Empty(t, h1)

	bc, err := parseBugcrowdData([]byte(`[]`))
	require.NoError(t, err)
	assert.Empty(t, bc)
}
