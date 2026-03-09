package jobs

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sampleBountyHubResponse = `{
  "data": [
    {
      "id": "abc-123",
      "title": "Fix memory leak in parser",
      "htmlURL": "https://github.com/org/repo/issues/42",
      "language": "Go",
      "repositoryFullName": "org/repo",
      "issueNumber": 42,
      "issueState": "open",
      "totalAmount": "500.00",
      "solved": false,
      "claimed": false,
      "createdAt": "2026-03-01T12:00:00Z"
    },
    {
      "id": "def-456",
      "title": "Add CSV export",
      "htmlURL": "https://github.com/other/lib/issues/7",
      "language": "Python",
      "repositoryFullName": "other/lib",
      "issueNumber": 7,
      "issueState": "open",
      "totalAmount": "120.50",
      "solved": false,
      "claimed": false,
      "createdAt": "2026-02-15T08:30:00Z"
    }
  ],
  "hasNextPage": false
}`

func TestParseBountyHubResponse(t *testing.T) {
	t.Parallel()
	bounties, hasNext, err := parseBountyHubResponse([]byte(sampleBountyHubResponse))
	require.NoError(t, err)
	assert.False(t, hasNext)
	require.Len(t, bounties, 2)

	b := bounties[0]
	assert.Equal(t, "Fix memory leak in parser", b.Title)
	assert.Equal(t, "org/repo", b.Org)
	assert.Equal(t, "https://github.com/org/repo/issues/42", b.URL)
	assert.Equal(t, "$500", b.Amount)
	assert.Equal(t, "USD", b.Currency)
	assert.Equal(t, []string{"Go"}, b.Skills)
	assert.Equal(t, "bountyhub", b.Source)
	assert.Equal(t, "#42", b.IssueNum)
	assert.NotEmpty(t, b.Posted)

	b2 := bounties[1]
	assert.Equal(t, "$120", b2.Amount)
	assert.Equal(t, []string{"Python"}, b2.Skills)
}

func TestParseBountyHubResponse_empty(t *testing.T) {
	t.Parallel()
	bounties, _, err := parseBountyHubResponse([]byte(`{"data":[],"hasNextPage":false}`))
	require.NoError(t, err)
	assert.Empty(t, bounties)
}
