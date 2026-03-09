package engine

// --- Job search types ---

type JobSearchInput struct {
	Query      string `json:"query" jsonschema:"Job search keywords (e.g. golang developer, data engineer)"`
	Location   string `json:"location,omitempty" jsonschema:"City, country, or Remote (e.g. Berlin, United States, Remote)"`
	Experience string `json:"experience,omitempty" jsonschema:"Experience level: internship, entry, associate, mid-senior, director, executive"`
	JobType    string `json:"job_type,omitempty" jsonschema:"Job type: full-time, part-time, contract, temporary"`
	Remote     string `json:"remote,omitempty" jsonschema:"Work type: onsite, hybrid, remote"`
	TimeRange  string `json:"time_range,omitempty" jsonschema:"Time posted: day, week, month"`
	Platform   string `json:"platform,omitempty" jsonschema:"Source filter: linkedin, greenhouse, lever, ats (greenhouse+lever), yc (workatastartup.com), hn (HN Who is Hiring), indeed, habr (Хабр Карьера), twitter (X/Twitter job tweets), google (Google Jobs), startup (yc+hn+ats), all (default)"`
	Salary     string `json:"salary,omitempty" jsonschema:"Minimum salary filter for LinkedIn: 40k+, 60k+, 80k+, 100k+, 120k+, 140k+, 160k+, 180k+, 200k+"`
	EasyApply  bool   `json:"easy_apply,omitempty" jsonschema:"LinkedIn only: filter to Easy Apply jobs (one-click apply)"`
	Language   string `json:"language,omitempty" jsonschema:"Language code for the answer (default: all)"`
	Limit    int    `json:"limit,omitempty" jsonschema:"Max results to return (default 15, max 50)"`
	Offset   int    `json:"offset,omitempty" jsonschema:"Skip first N results for pagination (default 0)"`
	Blacklist string `json:"blacklist,omitempty" jsonschema:"Comma-separated company names or keywords to exclude from results (e.g. Google, Meta, staffing)"`
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

// InterviewPrepInput is the input for interview_prep.
type InterviewPrepInput struct {
	Resume         string `json:"resume" jsonschema:"Your resume text"`
	JobDescription string `json:"job_description" jsonschema:"Job description to prepare for"`
	Company        string `json:"company,omitempty" jsonschema:"Company name (enriches questions with company context: tech stack, culture, news)"`
	Focus          string `json:"focus,omitempty" jsonschema:"Focus area: all (default), behavioral, technical, system_design"`
}

// PersonResearchInput is the input for person_research.
type PersonResearchInput struct {
	Name     string `json:"name" jsonschema:"Full name of the person to research"`
	Company  string `json:"company,omitempty" jsonschema:"Company they work at (helps narrow search)"`
	JobTitle string `json:"job_title,omitempty" jsonschema:"Their job title (helps narrow search)"`
}

// MasterResumeBuildInput is the input for master_resume_build.
type MasterResumeBuildInput struct {
	Resume string `json:"resume" jsonschema:"Full resume text — all experience, education, skills, projects, achievements, certifications"`
}

// ResumeGenerateInput is the input for resume_generate.
type ResumeGenerateInput struct {
	JobDescription string `json:"job_description" jsonschema:"Job description to tailor the resume for"`
	Company        string `json:"company,omitempty" jsonschema:"Company name (enriches with company research)"`
	Format         string `json:"format,omitempty" jsonschema:"Output format: text (default), markdown, json"`
}

// ResumeProfileInput is the input for resume_profile.
type ResumeProfileInput struct {
	Section string `json:"section,omitempty" jsonschema:"Optional: filter by section (experiences, skills, projects, achievements, educations, certifications, domains, methodologies, summary). Empty = return all."`
}

// ResumeMemorySearchInput is the input for resume_memory_search.
type ResumeMemorySearchInput struct {
	Query string `json:"query" jsonschema:"Semantic search query (e.g. 'distributed systems experience', 'Python projects')"`
	TopK  int    `json:"top_k,omitempty" jsonschema:"Number of results (default 10, max 30)"`
}

// ResumeMemoryAddInput is the input for resume_memory_add.
type ResumeMemoryAddInput struct {
	Content string `json:"content" jsonschema:"Text to store (career goal, preference, note about experience, etc.)"`
	Type    string `json:"type,omitempty" jsonschema:"Category: note, goal, preference, skill_context, experience_detail (default: note)"`
}

// ResumeMemoryUpdateInput is the input for resume_memory_update.
type ResumeMemoryUpdateInput struct {
	MemoryID string `json:"memory_id" jsonschema:"ID of the memory to update (from resume_memory_search results)"`
	Content  string `json:"content" jsonschema:"New content to replace the existing memory"`
}

// ProjectShowcaseInput is the input for project_showcase.
type ProjectShowcaseInput struct {
	Projects   string `json:"projects" jsonschema:"Project descriptions, GitHub URLs, or resume section with projects"`
	TargetRole string `json:"target_role,omitempty" jsonschema:"Target role to tailor narratives for (e.g. backend engineer)"`
}

// PitchGenerateInput is the input for pitch_generate.
type PitchGenerateInput struct {
	Resume     string `json:"resume" jsonschema:"Your resume text"`
	TargetRole string `json:"target_role" jsonschema:"Target role or position title"`
	Company    string `json:"company,omitempty" jsonschema:"Company name (enriches with company research for why-this-company answer)"`
}

// SkillGapInput is the input for skill_gap.
type SkillGapInput struct {
	Resume         string `json:"resume" jsonschema:"Your resume text"`
	JobDescription string `json:"job_description" jsonschema:"Target job description to analyze gaps against"`
}

// ApplicationPrepInput is the input for application_prep.
type ApplicationPrepInput struct {
	Resume         string `json:"resume" jsonschema:"Your resume text"`
	JobDescription string `json:"job_description" jsonschema:"Job description to apply for"`
	Company        string `json:"company,omitempty" jsonschema:"Company name (enriches with company research)"`
	Tone           string `json:"tone,omitempty" jsonschema:"Cover letter tone: professional (default), friendly, concise"`
}

// OfferCompareInput is the input for offer_compare.
type OfferCompareInput struct {
	Offers     string `json:"offers" jsonschema:"Describe 2+ job offers to compare (company, role, salary, equity, benefits, WLB, growth, location)"`
	Priorities string `json:"priorities,omitempty" jsonschema:"Your priorities: e.g. salary, remote, growth, WLB (helps weight the comparison)"`
}

// NegotiationPrepInput is the input for negotiation_prep.
type NegotiationPrepInput struct {
	Role         string `json:"role" jsonschema:"Job title you are negotiating for"`
	Company      string `json:"company,omitempty" jsonschema:"Company name (enriches with salary research data)"`
	Location     string `json:"location,omitempty" jsonschema:"Job location (for salary benchmarks)"`
	CurrentOffer string `json:"current_offer" jsonschema:"Current offer details: salary, equity, benefits, signing bonus"`
	TargetComp   string `json:"target_comp,omitempty" jsonschema:"Your target compensation (what you want to negotiate to)"`
	Leverage     string `json:"leverage,omitempty" jsonschema:"Your leverage: competing offers, unique skills, market demand"`
}

// --- Bounty search types ---

// BountySearchInput is the input for the bounty_search tool.
type BountySearchInput struct {
	Query     string   `json:"query,omitempty" jsonschema:"Search keywords to filter bounties (e.g. golang, rust, MCP, CLI). Empty returns all open bounties."`
	Language  string   `json:"language,omitempty" jsonschema:"Language code for the answer (default: all)"`
	MinAmount int      `json:"min_amount,omitempty" jsonschema:"Minimum bounty amount in USD (e.g. 500)"`
	Skills    []string `json:"skills,omitempty" jsonschema:"Filter by technologies (e.g. [Go, Rust]). Bounty must match at least one skill."`
}

// BountyListing is a structured representation of an open-source bounty.
type BountyListing struct {
	Title    string   `json:"title"`
	Org      string   `json:"org"`
	URL      string   `json:"url"`
	Amount   string   `json:"amount"`
	Currency string   `json:"currency,omitempty"`
	Skills   []string `json:"skills,omitempty"`
	Source   string   `json:"source"`
	IssueNum string   `json:"issue_num,omitempty"`
	Posted    string   `json:"posted,omitempty"`
	Relevance float32  `json:"relevance,omitempty"`
}

// BountySearchOutput is the structured output for bounty_search.
type BountySearchOutput struct {
	Query    string          `json:"query"`
	Bounties []BountyListing `json:"bounties"`
	Summary  string          `json:"summary"`
}

// BountyAttemptInput is the input for the bounty_attempt tool.
type BountyAttemptInput struct {
	URL string `json:"url" jsonschema:"GitHub issue URL of the bounty to attempt (e.g. https://github.com/org/repo/issues/123)"`
}

// BountyAttemptOutput is the output for the bounty_attempt tool.
type BountyAttemptOutput struct {
	URL        string `json:"url"`
	CommentURL string `json:"comment_url"`
	Status     string `json:"status"`
}

// BountyAnalyzeInput is the input for the bounty_analyze tool.
type BountyAnalyzeInput struct {
	URL string `json:"url" jsonschema:"GitHub issue URL of the bounty to analyze (e.g. https://github.com/org/repo/issues/123)"`
}

// BountyAnalysis is the LLM-generated complexity analysis.
type BountyAnalysis struct {
	Title       string   `json:"title"`
	Amount      string   `json:"amount"`
	Complexity  int      `json:"complexity"`    // 1-5
	EstHours    string   `json:"est_hours"`     // e.g. "4-8 hours"
	DollarPerHr string   `json:"dollar_per_hr"` // e.g. "$62-125/hr"
	Skills      []string `json:"skills_needed"`
	Summary     string   `json:"summary"`
	Verdict     string   `json:"verdict"` // "recommended", "fair", "avoid"
}

// ResumeEnrichInput is the input for resume_enrich.
type ResumeEnrichInput struct {
	Action  string `json:"action" jsonschema:"Action: 'start' to get enrichment questions, 'answer' to submit answers and apply enrichments"`
	Answers []struct {
		QuestionID string `json:"question_id" jsonschema:"ID of the question being answered"`
		Answer     string `json:"answer" jsonschema:"Your answer to the question"`
	} `json:"answers,omitempty" jsonschema:"Answers to enrichment questions (required when action='answer')"`
}
