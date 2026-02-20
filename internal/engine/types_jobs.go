package engine

// --- Job search types ---

type JobSearchInput struct {
	Query      string `json:"query" jsonschema:"Job search keywords (e.g. golang developer, data engineer)"`
	Location   string `json:"location,omitempty" jsonschema:"City, country, or Remote (e.g. Berlin, United States, Remote)"`
	Experience string `json:"experience,omitempty" jsonschema:"Experience level: internship, entry, associate, mid-senior, director, executive"`
	JobType    string `json:"job_type,omitempty" jsonschema:"Job type: full-time, part-time, contract, temporary"`
	Remote     string `json:"remote,omitempty" jsonschema:"Work type: onsite, hybrid, remote"`
	TimeRange  string `json:"time_range,omitempty" jsonschema:"Time posted: day, week, month"`
	Platform   string `json:"platform,omitempty" jsonschema:"Source filter: linkedin, greenhouse, lever, ats (greenhouse+lever), yc (workatastartup.com), hn (HN Who is Hiring), indeed, habr (Хабр Карьера), startup (yc+hn+ats), all (default)"`
	Salary     string `json:"salary,omitempty" jsonschema:"Minimum salary filter for LinkedIn: 40k+, 60k+, 80k+, 100k+, 120k+, 140k+, 160k+, 180k+, 200k+"`
	EasyApply  bool   `json:"easy_apply,omitempty" jsonschema:"LinkedIn only: filter to Easy Apply jobs (one-click apply)"`
	Language   string `json:"language,omitempty" jsonschema:"Language code for the answer (default: all)"`
}

// JobListing is a structured representation of a job listing.
type JobListing struct {
	Title          string   `json:"title"`
	Company        string   `json:"company"`
	URL            string   `json:"url"`
	JobID          string   `json:"job_id,omitempty"`
	Source         string   `json:"source,omitempty"`
	Location       string   `json:"location"`
	Salary         string   `json:"salary"`          // human-readable: "$80k–120k USD/yr"
	SalaryMin      *int     `json:"salary_min,omitempty"`      // numeric min (annual, in currency units)
	SalaryMax      *int     `json:"salary_max,omitempty"`      // numeric max
	SalaryCurrency string   `json:"salary_currency,omitempty"` // e.g. "USD", "EUR", "RUB"
	SalaryInterval string   `json:"salary_interval,omitempty"` // "year", "month", "hour"
	JobType        string   `json:"job_type"`
	Remote         string   `json:"remote"`
	Experience     string   `json:"experience,omitempty"`
	Skills         []string `json:"skills"`
	Description    string   `json:"description"`
	Posted         string   `json:"posted"`
}

// JobSearchOutput is the structured output for job_search.
type JobSearchOutput struct {
	Query   string       `json:"query"`
	Jobs    []JobListing `json:"jobs"`
	Summary string       `json:"summary"`
}

type FreelanceSearchInput struct {
	Query    string `json:"query" jsonschema:"Search query for freelance projects (e.g. golang API developer, React frontend)"`
	Platform string `json:"platform,omitempty" jsonschema:"Platform filter: upwork, freelancer, all (default: all)"`
	Language string `json:"language,omitempty" jsonschema:"Language code (default: all)"`
}

// FreelanceProject is a structured representation of a freelance project listing.
type FreelanceProject struct {
	Title       string   `json:"title"`
	URL         string   `json:"url"`
	Platform    string   `json:"platform"`
	Budget      string   `json:"budget"`
	Skills      []string `json:"skills"`
	Description string   `json:"description"`
	Posted      string   `json:"posted"`
	ClientInfo  string   `json:"client_info,omitempty"`
}

// FreelanceSearchOutput is the structured output for freelance_search.
type FreelanceSearchOutput struct {
	Query    string             `json:"query"`
	Projects []FreelanceProject `json:"projects"`
	Summary  string             `json:"summary"`
}

// RemoteWorkSearchInput is the input for the remote_work_search tool.
type RemoteWorkSearchInput struct {
	Query    string `json:"query" jsonschema:"Search keywords for remote jobs (e.g. golang, react developer, devops)"`
	Language string `json:"language,omitempty" jsonschema:"Language code for the answer (default: all)"`
}

// RemoteJobListing is a structured representation of a remote job listing.
type RemoteJobListing struct {
	Title    string   `json:"title"`
	Company  string   `json:"company"`
	URL      string   `json:"url"`
	Source   string   `json:"source"`
	Salary   string   `json:"salary"`
	Location string   `json:"location"`
	Tags     []string `json:"tags"`
	Posted   string   `json:"posted"`
	JobType  string   `json:"job_type"`
}

// RemoteWorkSearchOutput is the structured output for remote_work_search.
type RemoteWorkSearchOutput struct {
	Query   string             `json:"query"`
	Jobs    []RemoteJobListing `json:"jobs"`
	Summary string             `json:"summary"`
}

// --- Job match score types ---

// JobMatchScoreInput is the input for the job_match_score tool.
type JobMatchScoreInput struct {
	Resume   string `json:"resume" jsonschema:"Resume text to match against job listings"`
	Query    string `json:"query" jsonschema:"Job search keywords (e.g. golang developer, data engineer)"`
	Location string `json:"location,omitempty" jsonschema:"City, country, or Remote"`
	Platform string `json:"platform,omitempty" jsonschema:"Source filter: linkedin, indeed, yc, hn, all (default)"`
}

// JobMatchResult is a job listing annotated with a Jaccard keyword match score.
type JobMatchResult struct {
	Title            string   `json:"title"`
	Company          string   `json:"company,omitempty"`
	URL              string   `json:"url"`
	Location         string   `json:"location,omitempty"`
	Source           string   `json:"source,omitempty"`
	Snippet          string   `json:"snippet,omitempty"`
	MatchScore       float64  `json:"match_score"`        // 0–100 Jaccard keyword overlap
	MatchingKeywords []string `json:"matching_keywords"` // resume skills this job wants
	MissingKeywords  []string `json:"missing_keywords"`  // job keywords absent from resume
}

// JobMatchScoreOutput is the structured output for job_match_score.
type JobMatchScoreOutput struct {
	Query   string           `json:"query"`
	Jobs    []JobMatchResult `json:"jobs"`
	Summary string           `json:"summary"`
}

// SalaryResearchInput is the input for salary_research.
type SalaryResearchInput struct {
	Role       string `json:"role"`
	Location   string `json:"location,omitempty"`
	Experience string `json:"experience,omitempty"`
}

// CompanyResearchInput is the input for company_research.
type CompanyResearchInput struct {
	Company string `json:"company"`
}

// ResumeAnalyzeInput is the input for resume_analyze.
type ResumeAnalyzeInput struct {
	Resume          string `json:"resume"`
	JobDescription  string `json:"job_description"`
}

// CoverLetterInput is the input for cover_letter_generate.
type CoverLetterInput struct {
	Resume         string `json:"resume"`
	JobDescription string `json:"job_description"`
	Tone           string `json:"tone,omitempty"`
}

// ResumeTailorInput is the input for resume_tailor.
type ResumeTailorInput struct {
	Resume         string `json:"resume"`
	JobDescription string `json:"job_description"`
}
