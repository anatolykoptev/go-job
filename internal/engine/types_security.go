package engine

// SecurityProgram represents a bug bounty program from platforms like HackerOne, Bugcrowd, etc.
type SecurityProgram struct {
	Name      string   `json:"name"`
	Platform  string   `json:"platform"`   // hackerone, bugcrowd, intigriti, yeswehack, immunefi
	URL       string   `json:"url"`
	MaxBounty string   `json:"max_bounty"` // e.g. "$50,000"
	MinBounty string   `json:"min_bounty"` // e.g. "$100"
	Targets   []string `json:"targets"`    // in-scope domains/apps
	Type      string   `json:"type"`       // bug_bounty, vdp
	Managed   bool     `json:"managed"`    // managed/triaged by platform
}

// FreelanceJob represents a remote job or freelance gig.
type FreelanceJob struct {
	Title     string   `json:"title"`
	Company   string   `json:"company"`
	URL       string   `json:"url"`
	Tags      []string `json:"tags"`
	SalaryMin int      `json:"salary_min,omitempty"`
	SalaryMax int      `json:"salary_max,omitempty"`
	Source    string   `json:"source"` // remoteok, himalayas
	Posted    string   `json:"posted"`
	Location  string   `json:"location,omitempty"`
}
