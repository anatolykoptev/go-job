package jobs

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sampleRemoteOKFreelanceResponse = `[
  {"0": "meta", "legal": "https://remoteok.com"},
  {
    "slug": "remote-senior-go-developer",
    "company": "Acme Corp",
    "position": "Senior Go Developer",
    "tags": ["golang", "aws", "docker"],
    "salary_min": 80000,
    "salary_max": 120000,
    "url": "https://remoteok.com/remote-jobs/remote-senior-go-developer",
    "date": "2026-03-01T12:00:00+00:00",
    "location": "Worldwide"
  },
  {
    "slug": "remote-devops-engineer",
    "company": "Beta Inc",
    "position": "DevOps Engineer",
    "tags": ["devops", "kubernetes"],
    "salary_min": 0,
    "salary_max": 0,
    "url": "https://remoteok.com/remote-jobs/remote-devops-engineer",
    "date": "2026-02-28T10:00:00+00:00",
    "location": ""
  }
]`

func TestParseRemoteOKFreelanceResponse(t *testing.T) {
	t.Parallel()
	jobs, err := parseRemoteOKFreelanceResponse([]byte(sampleRemoteOKFreelanceResponse))
	require.NoError(t, err)
	require.Len(t, jobs, 2)

	j := jobs[0]
	assert.Equal(t, "Senior Go Developer", j.Title)
	assert.Equal(t, "Acme Corp", j.Company)
	assert.Equal(t, "https://remoteok.com/remote-jobs/remote-senior-go-developer", j.URL)
	assert.Equal(t, []string{"golang", "aws", "docker"}, j.Tags)
	assert.Equal(t, 80000, j.SalaryMin)
	assert.Equal(t, 120000, j.SalaryMax)
	assert.Equal(t, "remoteok", j.Source)
	assert.Equal(t, "2026-03-01T12:00:00+00:00", j.Posted)
	assert.Equal(t, "Worldwide", j.Location)

	j2 := jobs[1]
	assert.Equal(t, "DevOps Engineer", j2.Title)
	assert.Equal(t, "Beta Inc", j2.Company)
	assert.Equal(t, 0, j2.SalaryMin)
	assert.Equal(t, 0, j2.SalaryMax)
}

func TestParseRemoteOKFreelanceResponse_metaOnly(t *testing.T) {
	t.Parallel()
	jobs, err := parseRemoteOKFreelanceResponse([]byte(`[{"0": "meta"}]`))
	require.NoError(t, err)
	assert.Empty(t, jobs)
}

func TestParseRemoteOKFreelanceResponse_empty(t *testing.T) {
	t.Parallel()
	jobs, err := parseRemoteOKFreelanceResponse([]byte(`[]`))
	require.NoError(t, err)
	assert.Empty(t, jobs)
}
