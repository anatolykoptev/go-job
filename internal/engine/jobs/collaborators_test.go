package jobs

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sampleCollaboratorsResponse = `[
  {
    "id": "abc123",
    "title": "Fix auth flow",
    "bountyAmount": "250",
    "status": "ACTIVE",
    "isSolved": false,
    "githubIssueUrl": "https://github.com/org/repo/issues/10",
    "githubRepoOwner": "org",
    "githubRepoName": "repo",
    "githubIssueId": 10,
    "githubLabels": ["bounty"],
    "createdAt": "2026-01-15T12:00:00Z"
  },
  {
    "id": "def456",
    "title": "Solved task",
    "bountyAmount": "50",
    "status": "ACTIVE",
    "isSolved": true,
    "githubIssueUrl": "https://github.com/org/repo/issues/5",
    "githubRepoOwner": "org",
    "githubRepoName": "repo",
    "githubIssueId": 5,
    "githubLabels": [],
    "createdAt": "2026-01-10T12:00:00Z"
  }
]`

func TestParseCollaboratorsResponse(t *testing.T) {
	t.Parallel()
	bounties, err := parseCollaboratorsResponse([]byte(sampleCollaboratorsResponse))
	require.NoError(t, err)
	require.Len(t, bounties, 1) // solved one filtered out

	b := bounties[0]
	assert.Equal(t, "Fix auth flow", b.Title)
	assert.Equal(t, "org/repo", b.Org)
	assert.Equal(t, "https://github.com/org/repo/issues/10", b.URL)
	assert.Equal(t, "$250", b.Amount)
	assert.Equal(t, "USDC", b.Currency)
	assert.Equal(t, "collaborators", b.Source)
	assert.Equal(t, "#10", b.IssueNum)
}

func TestParseCollaboratorsResponse_empty(t *testing.T) {
	t.Parallel()
	bounties, err := parseCollaboratorsResponse([]byte(`[]`))
	require.NoError(t, err)
	assert.Empty(t, bounties)
}
