package engine

// Opportunity is the unified type returned by opportunity_search.
type Opportunity struct {
	Type    string   `json:"type"`    // "bounty", "security", "freelance"
	Title   string   `json:"title"`
	URL     string   `json:"url"`
	Reward  string   `json:"reward"`  // "$500", "Up to $50,000", "$80k-120k/yr"
	Source  string   `json:"source"`  // "algora", "hackerone", "remoteok", etc.
	Skills  []string `json:"skills"`
	Posted  string   `json:"posted,omitempty"`
	Summary string   `json:"summary,omitempty"`
}

// OpportunitySearchInput is the input for opportunity_search.
type OpportunitySearchInput struct {
	Type  string `json:"type,omitempty" jsonschema:"Filter by type: bounty, security, freelance, all (default: all)"`
	Query string `json:"query,omitempty" jsonschema:"Search keywords to filter (e.g. golang, crypto, api). Empty returns all."`
}

// OpportunitySearchOutput is the output for opportunity_search.
type OpportunitySearchOutput struct {
	Query         string        `json:"query"`
	Opportunities []Opportunity `json:"opportunities"`
	Summary       string        `json:"summary"`
}

// OpportunityAnalyzeInput is the input for opportunity_analyze.
type OpportunityAnalyzeInput struct {
	URL string `json:"url" jsonschema:"URL of the opportunity to analyze. Auto-detects type from URL (GitHub issue = bounty, immunefi/hackerone/bugcrowd = security, remoteok/himalayas = freelance)."`
}

// OpportunityAnalysis is the unified analysis output.
type OpportunityAnalysis struct {
	Type    string `json:"type"`    // "bounty", "security", "freelance"
	Title   string `json:"title"`
	URL     string `json:"url"`
	Reward  string `json:"reward"`
	Verdict string `json:"verdict"` // "recommended", "fair", "avoid"
	Summary string `json:"summary"`
	Details any    `json:"details"` // type-specific: BountyAnalysis, SecurityAnalysis, etc.
}

// OpportunityClaimInput is the input for opportunity_claim.
type OpportunityClaimInput struct {
	URL string `json:"url" jsonschema:"URL of the opportunity to claim. For bounties: posts /attempt on GitHub issue. For security: no action (manual). For freelance: generates cover letter."`
}
