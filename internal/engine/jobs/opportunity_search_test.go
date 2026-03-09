package jobs

import (
	"testing"

	"github.com/anatolykoptev/go_job/internal/engine"
)

func TestBountyToOpportunity(t *testing.T) {
	b := engine.BountyListing{
		Title:  "Fix auth bug",
		URL:    "https://github.com/org/repo/issues/1",
		Amount: "$500",
		Source: "algora",
		Skills: []string{"Go", "Auth"},
		Posted: "2026-03-01",
	}

	o := bountyToOpportunity(b)
	if o.Type != "bounty" {
		t.Errorf("got type %q, want bounty", o.Type)
	}

	if o.Title != "Fix auth bug" {
		t.Errorf("got title %q", o.Title)
	}

	if o.Reward != "$500" {
		t.Errorf("got reward %q", o.Reward)
	}
}

func TestSecurityToOpportunity(t *testing.T) {
	s := engine.SecurityProgram{
		Name:      "Acme Corp",
		Platform:  "hackerone",
		URL:       "https://hackerone.com/acme",
		MaxBounty: "$50,000",
		MinBounty: "$100",
		Targets:   []string{"api.acme.com", "app.acme.com"},
	}

	o := securityToOpportunity(s)
	if o.Type != "security" {
		t.Errorf("got type %q, want security", o.Type)
	}

	if o.Reward != "$100 - $50,000" {
		t.Errorf("got reward %q", o.Reward)
	}

	if o.Source != "hackerone" {
		t.Errorf("got source %q", o.Source)
	}
}

func TestFreelanceToOpportunity(t *testing.T) {
	f := engine.FreelanceJob{
		Title:     "Go Backend Dev",
		Company:   "Startup Inc",
		URL:       "https://remoteok.com/jobs/123",
		SalaryMin: 80000,
		SalaryMax: 120000,
		Source:    "remoteok",
		Tags:     []string{"golang", "postgres"},
		Posted:   "2026-03-01",
	}

	o := freelanceToOpportunity(f)
	if o.Type != "freelance" {
		t.Errorf("got type %q, want freelance", o.Type)
	}

	if o.Title != "Go Backend Dev @ Startup Inc" {
		t.Errorf("got title %q", o.Title)
	}

	if o.Reward != "$80000-$120000" {
		t.Errorf("got reward %q", o.Reward)
	}
}

func TestFilterOpportunities(t *testing.T) {
	opps := []engine.Opportunity{
		{Title: "Go API Developer", Skills: []string{"golang"}},
		{Title: "React Frontend", Skills: []string{"react"}},
		{Title: "Security Audit", Skills: []string{"pentest", "golang"}},
	}

	filtered := filterOpportunities(opps, "golang")
	if len(filtered) != 2 {
		t.Errorf("got %d results, want 2", len(filtered))
	}
}

func TestFilterOpportunitiesEmpty(t *testing.T) {
	opps := []engine.Opportunity{
		{Title: "React Frontend", Skills: []string{"react"}},
	}

	filtered := filterOpportunities(opps, "golang")
	if len(filtered) != 0 {
		t.Errorf("got %d results, want 0", len(filtered))
	}
}

func TestFilterOpportunitiesAlias(t *testing.T) {
	opps := []engine.Opportunity{
		{Title: "Fix auth bug", Skills: []string{"Go", "Auth"}},
		{Title: "Build CLI", Skills: []string{"Rust"}},
		{Title: "Golang API", Skills: []string{"Python"}},
	}

	// "golang" should match "Go" skill (alias) and "Golang" in title
	filtered := filterOpportunities(opps, "golang")
	if len(filtered) != 2 {
		t.Errorf("got %d results, want 2 (alias 'go' + title 'Golang')", len(filtered))
	}

	// "go" should match "golang" skill (alias) and "Go" skill
	filtered = filterOpportunities(opps, "go")
	if len(filtered) != 2 {
		t.Errorf("got %d results for 'go', want 2", len(filtered))
	}
}
