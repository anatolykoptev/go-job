package engine

// --- Job search types ---

type JobSearchInput struct {
	Query      string `json:"query" jsonschema:"Job search keywords (e.g. golang developer, data engineer)"`
	Location   string `json:"location,omitempty" jsonschema:"City, country, or Remote (e.g. Berlin, United States, Remote)"`
	Experience string `json:"experience,omitempty" jsonschema:"Experience level: internship, entry, associate, mid-senior, director, executive"`
	JobType    string `json:"job_type,omitempty" jsonschema:"Job type: full-time, part-time, contract, temporary"`
	Remote     string `json:"remote,omitempty" jsonschema:"Work type: onsite, hybrid, remote"`
	TimeRange  string `json:"time_range,omitempty" jsonschema:"Time posted: day, week, month"`
	Platform   string `json:"platform,omitempty" jsonschema:"Source filter: linkedin, greenhouse, lever, ats (greenhouse+lever), yc (workatastartup.com), hn (HN Who is Hiring), indeed, habr (Хабр Карьера), twitter (X/Twitter job tweets), startup (yc+hn+ats), all (default)"`
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

// ResumeEnrichInput is the input for resume_enrich.
type ResumeEnrichInput struct {
	Action  string `json:"action" jsonschema:"Action: 'start' to get enrichment questions, 'answer' to submit answers and apply enrichments"`
	Answers []struct {
		QuestionID string `json:"question_id" jsonschema:"ID of the question being answered"`
		Answer     string `json:"answer" jsonschema:"Your answer to the question"`
	} `json:"answers,omitempty" jsonschema:"Answers to enrichment questions (required when action='answer')"`
}
