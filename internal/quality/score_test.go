package quality

import (
	"strings"
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

// ---------------------------------------------------------------------------
// #231: Salary parsing from description text
// ---------------------------------------------------------------------------

func TestSalaryInText(t *testing.T) {
	cases := []struct {
		name string
		desc string
		want bool
	}{
		{"USD $120k", "Salary: $120k - $180k USD per year", true},
		{"USD $5,000", "Compensation: $5,000/month", true},
		{"USD $1.5M", "Base salary $1.5M", true},
		{"EUR €80k", "Salary: €80k", true},
		{"GBP £60k", "Salary: £60k", true},
		{"RU руб", "от 200 000 руб до 300 000 руб", true},
		{"RU оклад", "Оклад: 150000 руб", true},
		{"range 120k-180k", "120k-180k depending on experience", true},
		{"Lever format", "**Salary:** $120000-$180000 USD", true},
		{"LinkedIn format", "**Salary:** 120000-180000 USD/year", true},
		{"no salary", "We are looking for a Go engineer with 5 years experience.", false},
		{"empty", "", false},
		{"short", "abc", false},
		{"50k users no false positive", "We have 50k users and growing", false}, // no currency symbol = no match
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := salaryInText(c.desc)
			if got != c.want {
				t.Fatalf("salaryInText(%q): want %v, got %v", c.desc, c.want, got)
			}
		})
	}
}

func TestHasSalary_TextFallback(t *testing.T) {
	// No structured salary fields, but salary in description text.
	in := Input{
		Description: "**Salary:** $120k - $180k USD per year",
	}
	r := Score(in)
	if r.Breakdown.Salary != 20 {
		t.Fatalf("salary from text: want 20 pts, got %d", r.Breakdown.Salary)
	}

	// Structured field takes priority.
	in2 := Input{
		SalaryMin:   100_000,
		Description: "No salary mentioned in text",
	}
	r2 := Score(in2)
	if r2.Breakdown.Salary != 20 {
		t.Fatalf("salary from field: want 20 pts, got %d", r2.Breakdown.Salary)
	}

	// Neither field nor text.
	in3 := Input{Description: "Just a job description with no salary info."}
	r3 := Score(in3)
	if r3.Breakdown.Salary != 0 {
		t.Fatalf("no salary: want 0 pts, got %d", r3.Breakdown.Salary)
	}
}

// ---------------------------------------------------------------------------
// #232: Structured sections detection
// ---------------------------------------------------------------------------

func TestStructuredSectionsScore(t *testing.T) {
	cases := []struct {
		name    string
		desc    string
		wantPts int
	}{
		{
			"no sections",
			"We are looking for a Go engineer. You will work on distributed systems. " +
				"We use Kubernetes and PostgreSQL. Apply now if interested.",
			0,
		},
		{
			"one section",
			"## Requirements\n- 5+ years Go experience\n- Distributed systems knowledge\n" +
				"We offer competitive salary and great team.",
			2,
		},
		{
			"two sections",
			"## Requirements\n- 5+ years Go\n## Benefits\n- Health insurance\n- Remote work",
			3,
		},
		{
			"three sections",
			"## Requirements\n- Go\n## Benefits\n- Remote\n## Tech Stack\n- Go, Rust, K8s",
			5,
		},
		{
			"five sections",
			"## About Us\nWe build dev tools.\n## Requirements\n- Go\n## Benefits\n- Remote\n" +
				"## Tech Stack\n- Go, Rust\n## Nice to Have\n- Kubernetes",
			5,
		},
		{
			"markdown bold headers",
			"**Requirements:**\n- Go experience\n**Benefits:**\n- Health insurance\n**Tech Stack:**\n- Go",
			5,
		},
		{
			"too short",
			"Req\nBen",
			0,
		},
		{
			"what you'll need",
			"## What You'll Need\n- 5+ years Go\n## What You'll Do\n- Build distributed systems\n" +
				"## About the Team\n- We are 10 engineers",
			5,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := structuredSectionsScore(c.desc)
			if got != c.wantPts {
				t.Fatalf("structuredSectionsScore(%s): want %d pts, got %d", c.name, c.wantPts, got)
			}
		})
	}
}

func TestDescriptionFactorCapped(t *testing.T) {
	// Long description (>2000 chars = 10 pts) + 3 sections (5 pts) → capped at 10.
	desc := strings.Repeat("We need a Go engineer with Rust experience. ", 50) +
		"## Requirements\n- Go\n## Benefits\n- Remote\n## Tech Stack\n- Go, Rust"
	r := Score(Input{Description: desc})
	if r.Breakdown.DescriptionLength > 10 {
		t.Fatalf("description factor should be capped at 10, got %d", r.Breakdown.DescriptionLength)
	}
	// StructuredSections should be zeroed after capping.
	if r.Breakdown.StructuredSections != 0 {
		t.Fatalf("StructuredSections should be zeroed after capping, got %d", r.Breakdown.StructuredSections)
	}
}

// ---------------------------------------------------------------------------
// #233: Spam/scam marker detection
// ---------------------------------------------------------------------------

func TestSpamPenalty_HardSignals(t *testing.T) {
	cases := []struct {
		name string
		in   Input
	}{
		{
			"earn $5000 per day",
			Input{Title: "Work From Home", Description: "Earn $5000 per day with no effort!"},
		},
		{
			"no experience needed",
			Input{Title: "Data Entry", Description: "No experience needed, start today!"},
		},
		{
			"be your own boss",
			Input{Title: "Business Opportunity", Description: "Be your own boss and earn from home."},
		},
		{
			"WFH + earn + dollar",
			Input{Title: "Remote Job", Description: "Work from home and earn $5000 weekly"},
		},
		{
			"immediate start + no experience",
			Input{Title: "Urgent Role", Description: "Immediate start available, no experience required."},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Score(c.in)
			if r.Breakdown.SpamPenalty >= 0 {
				t.Fatalf("hard spam signal should produce negative penalty, got %d", r.Breakdown.SpamPenalty)
			}
			if r.Breakdown.SpamPenalty < -15 {
				t.Fatalf("hard spam penalty should be at most -15, got %d", r.Breakdown.SpamPenalty)
			}
		})
	}
}

func TestSpamPenalty_SoftSignals(t *testing.T) {
	// Excessive exclamation marks (>5).
	in := Input{Title: "Software Engineer", Description: "Great opportunity!!! Amazing!!! Apply now!!!!!"}
	r := Score(in)
	if r.Breakdown.SpamPenalty >= 0 {
		t.Fatalf("excessive exclamation marks should produce negative penalty, got %d", r.Breakdown.SpamPenalty)
	}

	// ALL CAPS title.
	in2 := Input{Title: "URGENT SOFTWARE ENGINEER NEEDED IMMEDIATELY", Description: "A normal job description here."}
	r2 := Score(in2)
	if r2.Breakdown.SpamPenalty >= 0 {
		t.Fatalf("ALL CAPS title should produce negative penalty, got %d", r2.Breakdown.SpamPenalty)
	}

	// "urgent hiring" pressure tactic.
	in3 := Input{Title: "Developer", Description: "Urgent hiring for a Go developer. Join our team."}
	r3 := Score(in3)
	if r3.Breakdown.SpamPenalty >= 0 {
		t.Fatalf("urgent hiring should produce negative penalty, got %d", r3.Breakdown.SpamPenalty)
	}

	// "click here to apply" phishing pattern.
	in4 := Input{Title: "Remote Job", Description: "Great pay. Click here to apply now!"}
	r4 := Score(in4)
	if r4.Breakdown.SpamPenalty >= 0 {
		t.Fatalf("click here to apply should produce negative penalty, got %d", r4.Breakdown.SpamPenalty)
	}
}

func TestSpamPenalty_SoftCap(t *testing.T) {
	// Multiple soft signals: ALL CAPS title + excessive exclamations + urgent hiring.
	// Each soft = -5, capped at -10.
	in := Input{
		Title:       "URGENT HIRING SOFTWARE ENGINEER NEEDED",
		Description: "Great opportunity!!! Amazing!!! Apply now!!!!! Urgent hiring!",
	}
	r := Score(in)
	if r.Breakdown.SpamPenalty > -10 {
		t.Fatalf("soft spam penalty should be capped at -10, got %d", r.Breakdown.SpamPenalty)
	}
}

func TestSpamPenalty_HardAndSoftStack(t *testing.T) {
	// Hard (-15) + soft (-10 capped) = -25.
	in := Input{
		Title:       "URGENT HIRING ENGINEER NEEDED",
		Description: "No experience needed!!! Earn $5000 per day!!! Click here to apply!!!!!",
	}
	r := Score(in)
	if r.Breakdown.SpamPenalty != -25 {
		t.Fatalf("hard+soft stack: want -25, got %d", r.Breakdown.SpamPenalty)
	}
}

func TestSpamPenalty_NoSpam(t *testing.T) {
	// Legitimate job posting — no spam penalty.
	in := Input{
		Title:       "Senior Go Engineer",
		Company:     "Acme Corp",
		Description: "We are looking for a senior Go engineer with 5+ years of experience in distributed systems.",
	}
	r := Score(in)
	if r.Breakdown.SpamPenalty != 0 {
		t.Fatalf("legitimate job should have 0 spam penalty, got %d", r.Breakdown.SpamPenalty)
	}
}

func TestSpamPenalty_DropsScoreBelowGate(t *testing.T) {
	// A posting that would score ~35 without spam penalty, but has a hard scam signal.
	// With -15 penalty, it drops to ~20, below the default quality gate of 30.
	in := Input{
		Title:       "Data Entry Clerk",
		Company:     "Acme Corp",
		URL:         "https://boards.greenhouse.io/acme/jobs/1",
		Description: "No experience needed. We provide training. Competitive salary $40k.",
		Source:      "greenhouse",
	}
	r := Score(in)
	if r.Breakdown.SpamPenalty >= 0 {
		t.Fatalf("expected negative spam penalty, got %d", r.Breakdown.SpamPenalty)
	}
	// The score should be lower than it would be without the penalty.
	// Without penalty: salary(20) + direct_apply(15) + not_agency(15) + source(15) + has_desc(10) = 75
	// With -15 hard penalty: 60
	if r.Score > 60 {
		t.Fatalf("spam penalty should drop score below 60, got %d", r.Score)
	}
}

func TestIsAllCapSTitle(t *testing.T) {
	cases := []struct {
		title string
		want  bool
	}{
		{"URGENT HIRING NOW", true},
		{"Software Engineer", false},
		{"senior go engineer", false},
		{"", false},
		{"short", false}, // < 10 chars
		{"SENIOR GO ENGINEER AT ACME CORP", true},
		{"Senior Go Engineer at Acme Corp", false},
	}
	for _, c := range cases {
		got := isAllCapSTitle(c.title)
		if got != c.want {
			t.Fatalf("isAllCapSTitle(%q): want %v, got %v", c.title, c.want, got)
		}
	}
}
