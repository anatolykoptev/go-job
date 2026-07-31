package jobs

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sampleHimalayasResponse = `{
  "jobs": [
    {
      "title": "Backend Engineer (Go)",
      "companyName": "CloudCo",
      "applicationLink": "https://himalayas.app/companies/cloudco/jobs/backend-engineer",
      "categories": ["Engineering", "Backend"],
      "seniority": ["Senior"],
      "minSalary": 90000,
      "maxSalary": 140000,
      "salaryPeriod": "annual",
      "pubDate": "2026-03-05",
      "excerpt": "Build scalable Go microservices."
    },
    {
      "title": "Platform Engineer",
      "companyName": "DevOps Ltd",
      "applicationLink": "https://himalayas.app/companies/devops-ltd/jobs/platform-engineer",
      "categories": ["DevOps"],
      "seniority": [],
      "minSalary": 0,
      "maxSalary": 0,
      "salaryPeriod": "annual",
      "pubDate": "2026-03-04",
      "excerpt": "Manage Kubernetes clusters."
    }
  ],
  "totalCount": 2
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
	jobs, err := parseHimalayasResponse([]byte(`{"jobs": [], "totalCount": 0}`))
	require.NoError(t, err)
	assert.Empty(t, jobs)
}

func TestParseHimalayasResponse_nullJobs(t *testing.T) {
	t.Parallel()
	jobs, err := parseHimalayasResponse([]byte(`{"jobs": null, "totalCount": 0}`))
	require.NoError(t, err)
	assert.Empty(t, jobs)
}

func TestParseHimalayasResponse_numericPubDate(t *testing.T) {
	t.Parallel()
	resp := `{"jobs": [{"title": "Engineer", "companyName": "Co", "applicationLink": "https://example.com/job", "categories": [], "seniority": [], "minSalary": 0, "maxSalary": 0, "pubDate": 1741305600000, "excerpt": ""}], "totalCount": 1}`
	jobs, err := parseHimalayasResponse([]byte(resp))
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	// 1741305600000ms = 2025-03-07T00:00:00Z
	assert.Equal(t, "2025-03-07T00:00:00Z", jobs[0].Posted)
}

// TestParseHimalayasResponse_RealFixture guards the two total-loss himalayas
// decode bugs against a REAL captured response body (testdata/himalayas_response_real.json,
// captured from the live himalayas.app/jobs/api via a Cloudflare-cleared browser
// on 2026-07-30). The fixture is verbatim real structure; the only synthetic edit
// is job[0].minSalary/maxSalary = 26.5 — the exact fractional value the production
// log reported ("cannot unmarshal number 26.5 into ... minSalary of type int").
//
// Bug 1 (field type): minSalary/maxSalary were `int`; encoding/json aborts the
// WHOLE Unmarshal on the first fractional value → every himalayas job lost.
// Bug 2 (field name): the URL field was tagged `applicationUrl` but the real
// API field is `applicationLink` → ApplicationURL always "" → the parser's
// `if hj.ApplicationURL == "" { continue }` skipped EVERY job even after a
// salary fix.
// Bug 3 (salary period): salaryPeriod "hourly" was dropped, writing an hourly
// rate (26.5 → 27) into the annual-salary field. Now hourly is normalised to
// annual (×2080) so both hourly and annual listings are comparable.
//
// Revert-red:
//   - Restore MinSalary/MaxSalary to `int` → json.Unmarshal fails on 26.5 →
//     test fails with a parse error (or 0 jobs).
//   - Restore the tag to `applicationUrl` → all 3 jobs skipped (len 0) → test
//     fails with "expected 3 jobs, got 0".
//   - Drop the salaryPeriod handling (annualizeHimalayasSalary → plain round) →
//     job[0].SalaryMin = 27 instead of 55120 → test fails.
func TestParseHimalayasResponse_RealFixture(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile(filepath.Join("testdata", "himalayas_response_real.json"))
	require.NoError(t, err)

	jobs, err := parseHimalayasResponse(data)
	require.NoError(t, err, "fractional salary 26.5 must not abort the unmarshal")
	require.Len(t, jobs, 3, "all 3 jobs must parse; applicationLink (not applicationUrl) must map")

	// job[0]: fractional hourly salary 26.5 → annualised: 26.5 × 2080 = 55120.
	// URL from applicationLink.
	assert.Equal(t, "https://himalayas.app/companies/radpartners-avature/jobs/remote-overnight-general-radiologist-7-on-14-off-radiology-partners-indiana-no", jobs[0].URL)
	assert.Equal(t, 55120, jobs[0].SalaryMin, "26.5 hourly × 2080 = 55120 annual")
	assert.Equal(t, 55120, jobs[0].SalaryMax)
	assert.Equal(t, "Radiology Partners", jobs[0].Company)

	// job[1]: integer annual salary 120000-200000 (salaryPeriod "annual").
	assert.Equal(t, "https://himalayas.app/companies/lifelancer/jobs/senior-product-manager-medical-inquiry", jobs[1].URL)
	assert.Equal(t, 120000, jobs[1].SalaryMin)
	assert.Equal(t, 200000, jobs[1].SalaryMax)

	// job[2]: null salaries → 0 (must not abort, must not skip).
	assert.Equal(t, "https://himalayas.app/companies/relx/jobs/customer-experience-manager", jobs[2].URL)
	assert.Equal(t, 0, jobs[2].SalaryMin)
	assert.Equal(t, 0, jobs[2].SalaryMax)

	// Total decodes from the real "totalCount" key (not "total").
	var resp himalayasResponse
	require.NoError(t, json.Unmarshal(data, &resp))
	assert.Equal(t, 98881, resp.Total, "totalCount must decode into himalayasResponse.Total")
}

// TestAnnualizeHimalayasSalary_UnknownPeriodFailsClosed (F7) asserts that an
// unrecognised salaryPeriod (daily/weekly/monthly/…) returns 0 instead of
// passing the raw value through as annual. A monthly 8000 passed through would
// become a plausible annual $8k that silently sinks the row in ranking with
// nothing logged — understatement, so nobody reports it. 0 already means
// "undisclosed" end to end.
//
// Revert-red: restore the pass-through default (`return int(math.Round(*v))`)
// → the monthly case returns 8000, not 0 → test fails.
func TestAnnualizeHimalayasSalary_UnknownPeriodFailsClosed(t *testing.T) {
	t.Parallel()
	v := 8000.0
	for _, period := range []string{"monthly", "daily", "weekly", "quarterly", "fortnightly"} {
		got := annualizeHimalayasSalary(&v, period)
		assert.Equalf(t, 0, got, "period %q must fail closed to 0 (undisclosed), got %d", period, got)
	}
}

// TestAnnualizeHimalayasSalary_CaseInsensitivePeriod (F8) asserts that the
// period is matched case-insensitively — "Hourly"/"HOURLY" must annualise via
// ×2080, not fall into the fail-closed default. The live API could emit any
// casing; a case-sensitive switch would handle "Hourly" wrong-but-silently.
//
// Revert-red: drop the strings.ToLower wrapping → "Hourly" hits the default
// branch → under the fail-closed default it returns 0 (not 55120); under a
// restored pass-through default it returns 27 (not 55120). Either way the test
// fails.
func TestAnnualizeHimalayasSalary_CaseInsensitivePeriod(t *testing.T) {
	t.Parallel()
	hourly := 26.5 // matches the real-fixture job[0] fractional hourly rate
	want := 55120  // 26.5 × 2080 = 55120
	for _, period := range []string{"hourly", "Hourly", "HOURLY", "HoUrLy"} {
		got := annualizeHimalayasSalary(&hourly, period)
		assert.Equalf(t, want, got, "period %q must annualise to %d (×2080), got %d", period, want, got)
	}
}

// TestAnnualizeHimalayasSalary_YearlyAliasesAnnual (F11) asserts that "yearly"
// — the one string the live API would plausibly use as a synonym for "annual" —
// passes through as annual, not fail-closed to 0. If Himalayas ever renames
// annual→yearly, the fail-closed branch would silently swallow every salary
// with nothing logged. "yearly" is semantically identical to "annual"; the
// fail-closed branch is for genuinely unknown periods where we cannot
// confidently assert a conversion factor.
//
// Revert-red: drop "yearly" from the annual case → it falls into the default
// branch → returns 0 (not 120000) → test fails.
func TestAnnualizeHimalayasSalary_YearlyAliasesAnnual(t *testing.T) {
	t.Parallel()
	v := 120000.0
	for _, period := range []string{"yearly", "Yearly", "YEARLY"} {
		got := annualizeHimalayasSalary(&v, period)
		assert.Equalf(t, 120000, got, "period %q must alias to annual (120000), got %d", period, got)
	}
}

// TestAnnualizeHimalayasSalary_UnknownPeriodLogsWarn (F10) asserts that an
// unrecognised salaryPeriod emits a slog.Warn naming the period. Without this
// signal, an unrecognised period at scale silently NULLs every affected row's
// budget and nothing anywhere reveals it — the exact failure mode this PR
// exists to fix.
//
// Revert-red: remove the slog.Warn from the default branch → buf is empty →
// test fails with "expected WARN mentioning period".
func TestAnnualizeHimalayasSalary_UnknownPeriodLogsWarn(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	handler := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(oldLogger) })

	v := 8000.0
	got := annualizeHimalayasSalary(&v, "fortnightly")
	assert.Equal(t, 0, got, "unknown period must still fail closed to 0")

	// One anchored match over a SINGLE line, not two independent Contains:
	// the buffer is a process-global slog sink, so a foreign test's WARN could
	// satisfy a bare Contains("level=WARN") while this call satisfies
	// Contains("fortnightly") — and the test would still pass with this log
	// downgraded to INFO.
	out := buf.String()
	assert.Regexp(t, `level=WARN[^\n]*fortnightly`, out,
		"expected a single WARN line naming the unrecognised period, got: %s", out)
}
