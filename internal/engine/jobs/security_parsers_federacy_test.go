package jobs

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sampleFederacyData = `[
  {
    "id": "11f3b580-8ce4-4fcf-b750-4eb2f7c28bef",
    "name": "AngelList VDP",
    "offers_awards": false,
    "url": "https://www.federacy.com/angellist-vdp",
    "targets": {
      "in_scope": [
        {"type": "website", "target": "*.angellist.com"},
        {"type": "website", "target": "api.angellist.com"}
      ],
      "out_of_scope": [
        {"type": "website", "target": "Wellfound"}
      ]
    }
  },
  {
    "id": "b6d8dffd-33de-4ec9-849a-f98f42270b2d",
    "name": "BountyProg",
    "offers_awards": true,
    "url": "https://www.federacy.com/bounty-prog",
    "targets": {
      "in_scope": [
        {"type": "website", "target": "*.bounty.com"}
      ]
    }
  },
  {
    "id": "no-url-prog",
    "name": "No URL Program",
    "offers_awards": false,
    "url": "",
    "targets": {"in_scope": []}
  }
]`

func TestParseFederacyData_VDP(t *testing.T) {
	t.Parallel()
	programs, err := parseFederacyData([]byte(sampleFederacyData))
	require.NoError(t, err)
	// Empty-URL entry is skipped; 2 valid entries remain.
	require.Len(t, programs, 2)

	vdp := programs[0]
	assert.Equal(t, "AngelList VDP", vdp.Name)
	assert.Equal(t, "federacy", vdp.Platform)
	assert.Equal(t, "https://www.federacy.com/angellist-vdp", vdp.URL)
	assert.Equal(t, progTypeVDP, vdp.Type, "offers_awards=false must map to vdp")
	// Only in_scope targets included; out_of_scope is ignored.
	assert.Contains(t, vdp.Targets, "*.angellist.com")
	assert.Contains(t, vdp.Targets, "api.angellist.com")
	assert.NotContains(t, vdp.Targets, "Wellfound", "out_of_scope must not appear in Targets")
	// No bounty for VDP.
	assert.Empty(t, vdp.MaxBounty)
	assert.Empty(t, vdp.MinBounty)
}

func TestParseFederacyData_BugBounty(t *testing.T) {
	t.Parallel()
	programs, err := parseFederacyData([]byte(sampleFederacyData))
	require.NoError(t, err)
	require.Len(t, programs, 2)

	bb := programs[1]
	assert.Equal(t, "BountyProg", bb.Name)
	assert.Equal(t, progTypeBugBounty, bb.Type, "offers_awards=true must map to bug_bounty")
	assert.Contains(t, bb.Targets, "*.bounty.com")
}

func TestParseFederacyData_EmptyURL_Skipped(t *testing.T) {
	t.Parallel()
	programs, err := parseFederacyData([]byte(sampleFederacyData))
	require.NoError(t, err)
	for _, p := range programs {
		assert.NotEmpty(t, p.URL, "programs with empty URL must be skipped")
	}
}

func TestParseFederacyData_EmptyInput(t *testing.T) {
	t.Parallel()
	programs, err := parseFederacyData([]byte(`[]`))
	require.NoError(t, err)
	assert.Empty(t, programs)
}

func TestParseFederacyData_BadJSON(t *testing.T) {
	t.Parallel()
	_, err := parseFederacyData([]byte(`not json`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "federacy")
}
