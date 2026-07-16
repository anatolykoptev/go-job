package quality

import (
	"testing"
	"time"
)

func ptrTime(t time.Time) *time.Time { return &t }

func TestScore_NilInput(t *testing.T) {
	r := Score(Input{})
	if r.Score != 0 {
		t.Fatalf("empty input: want score 0, got %d", r.Score)
	}
	if r.Verdict != VerdictSkip {
		t.Fatalf("empty input: want verdict %q, got %q", VerdictSkip, r.Verdict)
	}
}

func TestScore_PerfectJob(t *testing.T) {
	in := Input{
		Title:       "Senior Go Engineer",
		Company:     "Acme Corp",
		URL:         "https://boards.greenhouse.io/acme/jobs/123",
		Description: string(make([]byte, 2500)),
		Source:      "greenhouse",
		SalaryMin:   120000,
		SalaryMax:   180000,
		PostedAt:    ptrTime(time.Now().Add(-1 * time.Hour)),
	}
	r := Score(in)
	if r.Score != 100 {
		t.Fatalf("perfect job: want score 100, got %d (breakdown: %+v)", r.Score, r.Breakdown)
	}
	if r.Verdict != VerdictHigh {
		t.Fatalf("perfect job: want verdict %q, got %q", VerdictHigh, r.Verdict)
	}
}

func TestHasSalary(t *testing.T) {
	cases := []struct {
		name    string
		in      Input
		want    bool
		wantPts int
	}{
		{"min only", Input{SalaryMin: 80_000, Description: "x"}, true, 20},
		{"max only", Input{SalaryMax: 120_000, Description: "x"}, true, 20},
		{"both zero", Input{Description: "x"}, false, 0},
		{"both negative", Input{SalaryMin: -1, SalaryMax: -1, Description: "x"}, false, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Score(c.in)
			if r.Breakdown.Salary != c.wantPts {
				t.Fatalf("salary points: want %d, got %d", c.wantPts, r.Breakdown.Salary)
			}
		})
	}
}

func TestHasDirectApply(t *testing.T) {
	cases := []struct {
		url     string
		want    bool
		wantPts int
	}{
		{"https://boards.greenhouse.io/acme/jobs/1", true, 15},
		{"https://jobs.lever.co/acme/abc", true, 15},
		{"https://www.ashbyhq.com/acme/xyz", true, 15},
		{"https://www.workatastartup.com/jobs/1", true, 15},
		{"https://linkedin.com/jobs/view/123", false, 0},
		{"https://example.com/careers/apply-now", true, 15},
		{"https://example.com/careers", false, 0},
		{"", false, 0},
	}
	for _, c := range cases {
		t.Run(c.url, func(t *testing.T) {
			r := Score(Input{URL: c.url, Description: "x"})
			if r.Breakdown.DirectApply != c.wantPts {
				t.Fatalf("direct_apply points for %q: want %d, got %d", c.url, c.wantPts, r.Breakdown.DirectApply)
			}
		})
	}
}

func TestFreshnessScore(t *testing.T) {
	cases := []struct {
		name    string
		posted  *time.Time
		wantPts int
	}{
		{"nil", nil, 0},
		{"zero", ptrTime(time.Time{}), 0},
		{"1h ago", ptrTime(time.Now().Add(-1 * time.Hour)), 15},
		{"23h ago", ptrTime(time.Now().Add(-23 * time.Hour)), 15},
		{"30h ago", ptrTime(time.Now().Add(-30 * time.Hour)), 10},
		{"70h ago", ptrTime(time.Now().Add(-70 * time.Hour)), 10},
		{"3d ago", ptrTime(time.Now().Add(-3 * 24 * time.Hour)), 5},
		{"6d ago", ptrTime(time.Now().Add(-6 * 24 * time.Hour)), 5},
		{"8d ago", ptrTime(time.Now().Add(-8 * 24 * time.Hour)), 0},
		{"future", ptrTime(time.Now().Add(1 * time.Hour)), 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Score(Input{PostedAt: c.posted, Description: "x"})
			if r.Breakdown.Freshness != c.wantPts {
				t.Fatalf("freshness %s: want %d pts, got %d", c.name, c.wantPts, r.Breakdown.Freshness)
			}
		})
	}
}

func TestDescriptionLengthScore(t *testing.T) {
	cases := []struct {
		name    string
		desc    string
		wantPts int
	}{
		{"empty", "", 0},
		{"short", "abc", 0},
		{"201 chars", string(make([]byte, 201)), 5},
		{"501 chars", string(make([]byte, 501)), 7},
		{"2001 chars", string(make([]byte, 2001)), 10},
		{"whitespace only", "   \n\t  ", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Score(Input{Description: c.desc})
			if r.Breakdown.DescriptionLength != c.wantPts {
				t.Fatalf("desc length %s: want %d pts, got %d", c.name, c.wantPts, r.Breakdown.DescriptionLength)
			}
		})
	}
}

func TestHasSubstantiveDescription(t *testing.T) {
	cases := []struct {
		desc    string
		wantPts int
	}{
		{"", 0},
		{"short", 0},
		{string(make([]byte, 100)), 0},
		{string(make([]byte, 101)), 10},
		{string(make([]byte, 500)), 10},
	}
	for _, c := range cases {
		r := Score(Input{Description: c.desc})
		if r.Breakdown.HasDescription != c.wantPts {
			t.Fatalf("has_description for len %d: want %d pts, got %d", len(c.desc), c.wantPts, r.Breakdown.HasDescription)
		}
	}
}

func TestIsAgency_ByDomain(t *testing.T) {
	cases := []struct {
		url     string
		agency  bool
		wantPts int
	}{
		{"https://roberthalf.com/jobs/1", true, 0},
		{"https://www.aerotek.com/jobs", true, 0},
		{"https://boards.greenhouse.io/acme", false, 15},
		{"https://example.com", false, 15},
	}
	for _, c := range cases {
		t.Run(c.url, func(t *testing.T) {
			r := Score(Input{URL: c.url, Description: "x"})
			if r.Breakdown.NotAgency != c.wantPts {
				t.Fatalf("not_agency for %q: want %d pts, got %d", c.url, c.wantPts, r.Breakdown.NotAgency)
			}
		})
	}
}

func TestIsAgency_ByCompanyName(t *testing.T) {
	cases := []struct {
		company string
		agency  bool
		wantPts int
	}{
		{"Acme Corp", false, 15},
		{"Tech Staffing Solutions", true, 0},
		{"Global Recruiting Partners", true, 0},
		// "Robert Half Technology" is flagged via domain (roberthalf.com), not name tokens.
		{"Robert Half Technology", false, 15},
		{"Workforce Solutions Inc", true, 0},
		{"Direct Placement Agency", true, 0},
		{"Talent Acquisition Group", true, 0},
		{"", false, 15}, // empty company → not agency
	}
	for _, c := range cases {
		t.Run(c.company, func(t *testing.T) {
			r := Score(Input{Company: c.company, Description: "x"})
			if r.Breakdown.NotAgency != c.wantPts {
				t.Fatalf("not_agency for company %q: want %d pts, got %d", c.company, c.wantPts, r.Breakdown.NotAgency)
			}
		})
	}
}

func TestIsAgency_TokenStandalone(t *testing.T) {
	// "talent" as a substring of a non-agency name should NOT trigger.
	r := Score(Input{Company: "TalentLabs", Description: "x"})
	if r.Breakdown.NotAgency != 15 {
		t.Fatalf("TalentLabs should NOT be flagged agency (substring), got not_agency=%d", r.Breakdown.NotAgency)
	}
	// "talent" as a standalone word SHOULD trigger.
	r = Score(Input{Company: "Talent Labs Inc", Description: "x"})
	if r.Breakdown.NotAgency != 0 {
		t.Fatalf("Talent Labs Inc should be flagged agency (standalone word), got not_agency=%d", r.Breakdown.NotAgency)
	}
}

func TestSourceQualityScore(t *testing.T) {
	cases := []struct {
		source  string
		wantPts int
	}{
		{"greenhouse", 15},
		{"lever", 15},
		{"ashby", 15},
		{"yc", 15},
		{"workatastartup", 15},
		{"linkedin", 10},
		{"indeed", 10},
		{"hn", 5},
		{"twitter", 5},
		{"", 0},
	}
	for _, c := range cases {
		t.Run(c.source, func(t *testing.T) {
			r := Score(Input{Source: c.source, Description: "x"})
			if r.Breakdown.SourceQuality != c.wantPts {
				t.Fatalf("source_quality for %q: want %d pts, got %d", c.source, c.wantPts, r.Breakdown.SourceQuality)
			}
		})
	}
}

func TestVerdictFromScore(t *testing.T) {
	cases := []struct {
		score int
		want  string
	}{
		{100, VerdictHigh},
		{50, VerdictHigh},
		{49, VerdictMedium},
		{25, VerdictMedium},
		{24, VerdictLow},
		{10, VerdictLow},
		{9, VerdictSkip},
		{0, VerdictSkip},
	}
	for _, c := range cases {
		got := verdictFromScore(c.score)
		if got != c.want {
			t.Fatalf("verdictFromScore(%d): want %q, got %q", c.score, c.want, got)
		}
	}
}

func TestScore_ClampedTo100(t *testing.T) {
	// Construct an input that would exceed 100 if not clamped.
	// All factors max out at exactly 100, so this is a sanity check.
	in := Input{
		Title:       "Senior Go Engineer",
		Company:     "Acme Corp",
		URL:         "https://boards.greenhouse.io/acme/jobs/123?apply=true",
		Description: string(make([]byte, 3000)),
		Source:      "greenhouse",
		SalaryMin:   120000,
		SalaryMax:   180000,
		PostedAt:    ptrTime(time.Now().Add(-30 * time.Minute)),
	}
	r := Score(in)
	if r.Score > 100 {
		t.Fatalf("score should be clamped to 100, got %d", r.Score)
	}
	if r.Score < 0 {
		t.Fatalf("score should be >= 0, got %d", r.Score)
	}
}

func TestFromHuntJob(t *testing.T) {
	posted := time.Now().Add(-2 * time.Hour)
	in := JobInput{
		Title:       "Backend Engineer",
		Company:     "Acme",
		URL:         "https://boards.greenhouse.io/acme/jobs/1",
		Description: "A great job with lots of detail here.",
		Source:      "greenhouse",
		SalaryMin:   100_000,
		SalaryMax:   150_000,
		PostedAt:    &posted,
	}
	qi := FromHuntJob(in)
	if qi.Title != in.Title || qi.URL != in.URL || qi.SalaryMin != in.SalaryMin {
		t.Fatalf("FromHuntJob did not copy fields correctly: %+v", qi)
	}
	if qi.PostedAt == nil || !qi.PostedAt.Equal(posted) {
		t.Fatalf("FromHuntJob PostedAt not copied correctly")
	}
}
