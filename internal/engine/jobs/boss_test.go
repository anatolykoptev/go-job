package jobs

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sampleBossResponse = `[
  {
    "gid": "github/MDU6SXNzdWU0NjM4NTI2MzM=",
    "hId": "kistek/boss-demo#3",
    "sByC": {"EUR": 1200, "USD": 1234},
    "status": "open",
    "title": "Demo GitHub Issue with Bounty",
    "url": "https://github.com/kistek/boss-demo/issues/3",
    "usd": 2434
  },
  {
    "gid": "github/other123",
    "hId": "org/repo#15",
    "sByC": {"USD": 500},
    "status": "open",
    "title": "Fix login bug",
    "url": "https://github.com/org/repo/issues/15",
    "usd": 500
  }
]`

func TestParseBossResponse(t *testing.T) {
	t.Parallel()
	bounties, err := parseBossResponse([]byte(sampleBossResponse))
	require.NoError(t, err)
	require.Len(t, bounties, 2)

	b := bounties[0]
	assert.Equal(t, "Demo GitHub Issue with Bounty", b.Title)
	assert.Equal(t, "kistek/boss-demo", b.Org)
	assert.Equal(t, "https://github.com/kistek/boss-demo/issues/3", b.URL)
	assert.Equal(t, "$2,434", b.Amount)
	assert.Equal(t, "USD", b.Currency)
	assert.Equal(t, "boss", b.Source)
	assert.Equal(t, "#3", b.IssueNum)

	b2 := bounties[1]
	assert.Equal(t, "$500", b2.Amount)
	assert.Equal(t, "#15", b2.IssueNum)
}

func TestParseBossResponse_empty(t *testing.T) {
	t.Parallel()
	bounties, err := parseBossResponse([]byte(`[]`))
	require.NoError(t, err)
	assert.Empty(t, bounties)
}
