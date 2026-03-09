package jobs

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sampleImmunefiResponse = `[
  {
    "project": "Chainlink",
    "slug": "chainlink",
    "maxBounty": 3000000,
    "ecosystem": ["ETH", "Solana"],
    "launchDate": "2021-05-11T05:00:00.000Z",
    "assets": [
      {
        "url": "https://github.com/smartcontractkit/chainlink",
        "type": "smart_contract"
      },
      {
        "url": "https://data.chain.link/",
        "type": "websites_and_applications"
      }
    ]
  },
  {
    "project": "MakerDAO",
    "slug": "makerdao",
    "maxBounty": 100000,
    "ecosystem": ["ETH"],
    "launchDate": "2021-06-01T00:00:00.000Z",
    "assets": [
      {
        "url": "https://github.com/makerdao/dss",
        "type": "smart_contract"
      }
    ]
  },
  {
    "project": "NoAssets",
    "slug": "noassets",
    "maxBounty": 0,
    "ecosystem": [],
    "launchDate": "2023-01-01T00:00:00.000Z",
    "assets": []
  }
]`

func TestParseImmunefiResponse(t *testing.T) {
	t.Parallel()
	programs, err := parseImmunefiResponse([]byte(sampleImmunefiResponse))
	require.NoError(t, err)
	require.Len(t, programs, 3)

	p := programs[0]
	assert.Equal(t, "Chainlink", p.Name)
	assert.Equal(t, "immunefi", p.Platform)
	assert.Equal(t, "https://immunefi.com/bug-bounty/chainlink/", p.URL)
	assert.Equal(t, "$3,000,000", p.MaxBounty)
	assert.Equal(t, "bug_bounty", p.Type)
	assert.Contains(t, p.Targets, "https://github.com/smartcontractkit/chainlink")
	assert.Contains(t, p.Targets, "https://data.chain.link/")

	p2 := programs[1]
	assert.Equal(t, "MakerDAO", p2.Name)
	assert.Equal(t, "$100,000", p2.MaxBounty)

	p3 := programs[2]
	assert.Equal(t, "$0", p3.MaxBounty)
}

func TestParseImmunefiEmpty(t *testing.T) {
	t.Parallel()
	programs, err := parseImmunefiResponse([]byte(`[]`))
	require.NoError(t, err)
	assert.Empty(t, programs)
}
