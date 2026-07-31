package jobs

import (
	"testing"

	"github.com/anatolykoptev/go_job/internal/engine"
)

func TestSourceFromURL(t *testing.T) {
	cases := []struct {
		url      string
		expected string
	}{
		{"https://careers.un.org/jobSearchDescription/278362", "inspira"},
		{"https://careers.un.org/jobDescription/277335", "inspira"},
		{"https://estm.fa.em2.oraclecloud.com/hcmUI/CandidateExperience/en/sites/CX_1/job/34365", "undp"},
		{"https://jobs.undp.org/cj_view_jobs.cfm", "undp"},
		{"https://www.linkedin.com/jobs/view/12345", "linkedin"},
		{"https://boards.greenhouse.io/airbnb/jobs/12345", "greenhouse"},
		{"https://job-boards.greenhouse.io/airbnb/jobs/12345", "greenhouse"}, // broad match — previously missed
		{"https://boards-api.greenhouse.io/airbnb/jobs/12345", "greenhouse"}, // broad match
		// ATS URL carrying a linkedin marketing query param must classify as
		// its ATS provider, not linkedin. NormalizeURL strips the query before
		// classification and the ATS arms sit above the linkedin arm.
		{"https://boards.greenhouse.io/airbnb/jobs/12345?utm_source=linkedin", "greenhouse"},
		{"https://jobs.lever.co/stripe/abc?trk=linkedin_share", "lever"},
		{"https://jobs.ashbyhq.com/openai/role?utm_source=indeed", "ashby"},
		{"https://jobs.lever.co/stripe/abc", "lever"},
		{"https://jobs.ashbyhq.com/openai/role", "ashby"},
		{"https://www.workatastartup.com/jobs/12345", "yc"},
		{"https://www.ycombinator.com/jobs/12345", "yc"}, // broad ycombinator match (not news.ycombinator.com)
		{"https://news.ycombinator.com/item?id=12345", "hn"},
		{"https://www.indeed.com/viewjob?jk=abc", "indeed"},
		{"https://career.habr.com/vacancies/12345", "habr"},
		{"https://remoteok.com/remote-jobs/12345", "remoteok"},
		{"https://weworkremotely.com/remote-jobs/12345", "weworkremotely"},
		{"https://remotive.com/remote-jobs/12345", "remotive"},
		{"https://example.com/some-other-board", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := SourceFromURL(c.url); got != c.expected {
			t.Errorf("SourceFromURL(%q) = %q, want %q", c.url, got, c.expected)
		}
	}
}

func TestJobListingToHunt_SourceOverride(t *testing.T) {
	// LLM-emitted source is empty or "other" — URL-based override should kick in.
	cases := []struct {
		name    string
		input   engine.JobListing
		wantSrc string
	}{
		{
			name:    "empty source falls back to URL classifier",
			input:   engine.JobListing{URL: "https://careers.un.org/jobSearchDescription/123", Source: ""},
			wantSrc: "inspira",
		},
		{
			name:    "\"other\" source falls back to URL classifier",
			input:   engine.JobListing{URL: "https://estm.fa.em2.oraclecloud.com/hcmUI/CandidateExperience/en/sites/CX_1/job/9", Source: "other"},
			wantSrc: "undp",
		},
		{
			name:    "explicit source wins over URL classifier",
			input:   engine.JobListing{URL: "https://careers.un.org/jobSearchDescription/123", Source: "linkedin"},
			wantSrc: "linkedin",
		},
		{
			name:    "unknown URL with no source stays empty",
			input:   engine.JobListing{URL: "https://example.com/some-job", Source: ""},
			wantSrc: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := JobListingToHunt(c.input)
			if got.Source != c.wantSrc {
				t.Errorf("Source = %q, want %q", got.Source, c.wantSrc)
			}
		})
	}
}
