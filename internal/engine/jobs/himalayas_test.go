package jobs

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sampleHimalayasResponse = `{
  "jobs": [
    {
      "title": "Backend Engineer (Go)",
      "companyName": "CloudCo",
      "applicationUrl": "https://himalayas.app/companies/cloudco/jobs/backend-engineer",
      "categories": ["Engineering", "Backend"],
      "seniority": ["Senior"],
      "minSalary": 90000,
      "maxSalary": 140000,
      "pubDate": "2026-03-05",
      "excerpt": "Build scalable Go microservices."
    },
    {
      "title": "Platform Engineer",
      "companyName": "DevOps Ltd",
      "applicationUrl": "https://himalayas.app/companies/devops-ltd/jobs/platform-engineer",
      "categories": ["DevOps"],
      "seniority": [],
      "minSalary": 0,
      "maxSalary": 0,
      "pubDate": "2026-03-04",
      "excerpt": "Manage Kubernetes clusters."
    }
  ],
  "total": 2
}`

func TestParseHimalayasResponse(t *testing.T) {
	t.Parallel()
	jobs, err := parseHimalayasResponse([]byte(sampleHimalayasResponse))
	require.NoError(t, err)
	require.Len(t, jobs, 2)

	j := jobs[0]
	assert.Equal(t, "Backend Engineer (Go)", j.Title)
	assert.Equal(t, "CloudCo", j.Company)
	assert.Equal(t, "https://himalayas.app/companies/cloudco/jobs/backend-engineer", j.URL)
	assert.Equal(t, []string{"Engineering", "Backend", "Senior"}, j.Tags)
	assert.Equal(t, 90000, j.SalaryMin)
	assert.Equal(t, 140000, j.SalaryMax)
	assert.Equal(t, "himalayas", j.Source)
	assert.Equal(t, "2026-03-05", j.Posted)

	j2 := jobs[1]
	assert.Equal(t, "Platform Engineer", j2.Title)
	assert.Equal(t, "DevOps Ltd", j2.Company)
	assert.Equal(t, 0, j2.SalaryMin)
}

func TestParseHimalayasResponse_empty(t *testing.T) {
	t.Parallel()
	jobs, err := parseHimalayasResponse([]byte(`{"jobs": [], "total": 0}`))
	require.NoError(t, err)
	assert.Empty(t, jobs)
}

func TestParseHimalayasResponse_nullJobs(t *testing.T) {
	t.Parallel()
	jobs, err := parseHimalayasResponse([]byte(`{"jobs": null, "total": 0}`))
	require.NoError(t, err)
	assert.Empty(t, jobs)
}

func TestParseHimalayasResponse_numericPubDate(t *testing.T) {
	t.Parallel()
	resp := `{"jobs": [{"title": "Engineer", "companyName": "Co", "applicationUrl": "https://example.com/job", "categories": [], "seniority": [], "minSalary": 0, "maxSalary": 0, "pubDate": 1741305600000, "excerpt": ""}], "total": 1}`
	jobs, err := parseHimalayasResponse([]byte(resp))
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	// 1741305600000ms = 2025-03-07T00:00:00Z
	assert.Equal(t, "2025-03-07T00:00:00Z", jobs[0].Posted)
}
