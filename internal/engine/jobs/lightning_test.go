package jobs

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sampleLightningResponse = `[
  {
    "id": "0d8b05a9-b65e-4087-9ecf-9716557e0a08",
    "title": "Fix scanner issue",
    "html_url": "https://github.com/org/repo/issues/314",
    "is_closed": false,
    "winner_id": null,
    "total_reward_sats": 250000,
    "repository_data": {"full_name": "org/repo"},
    "created_at": "2025-10-28T10:20:52.853870"
  },
  {
    "id": "closed-one",
    "title": "Already solved",
    "html_url": "https://github.com/org/repo/issues/100",
    "is_closed": true,
    "winner_id": "some-winner",
    "total_reward_sats": 100000,
    "repository_data": {"full_name": "org/repo"},
    "created_at": "2025-09-01T00:00:00.000000"
  }
]`

func TestParseLightningResponse(t *testing.T) {
	t.Parallel()
	bounties, err := parseLightningResponse([]byte(sampleLightningResponse))
	require.NoError(t, err)
	require.Len(t, bounties, 1) // closed one filtered out

	b := bounties[0]
	assert.Equal(t, "Fix scanner issue", b.Title)
	assert.Equal(t, "org/repo", b.Org)
	assert.Equal(t, "https://github.com/org/repo/issues/314", b.URL)
	assert.Equal(t, "$200", b.Amount) // 250000 sats * 80000 / 100000000 = $200
	assert.Equal(t, "USD", b.Currency)
	assert.Equal(t, "lightning", b.Source)
	assert.Equal(t, "#314", b.IssueNum)
}

func TestParseLightningResponse_empty(t *testing.T) {
	t.Parallel()
	bounties, err := parseLightningResponse([]byte(`[]`))
	require.NoError(t, err)
	assert.Empty(t, bounties)
}
