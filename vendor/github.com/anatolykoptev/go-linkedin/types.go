package linkedin

import "time"

// YearMonth represents a month-precision date used in LinkedIn experiences/education.
type YearMonth struct {
	Year  int `json:"year"`
	Month int `json:"month"`
}

// Profile is a full LinkedIn profile with all sections.
type Profile struct {
	URN              string          `json:"urn"`
	FirstName        string          `json:"first_name"`
	LastName         string          `json:"last_name"`
	Headline         string          `json:"headline"`
	Location         string          `json:"location"`
	About            string          `json:"about"`
	Industry         string          `json:"industry"`
	ConnectionCount  int             `json:"connection_count"`
	FollowerCount    int             `json:"follower_count"`
	Experiences      []Experience    `json:"experiences,omitempty"`
	Educations       []Education     `json:"educations,omitempty"`
	Skills           []Skill         `json:"skills,omitempty"`
	Certifications   []Certification `json:"certifications,omitempty"`
	ContactInfo      *ContactInfo    `json:"contact_info,omitempty"`
	ProfileURL       string          `json:"profile_url"`
	PublicIdentifier string          `json:"public_identifier,omitempty"`
	Premium          bool            `json:"premium,omitempty"`
	Influencer       bool            `json:"influencer,omitempty"`
	Creator          bool            `json:"creator,omitempty"`
}

// Experience is a single work experience entry.
type Experience struct {
	Title       string     `json:"title"`
	Company     string     `json:"company"`
	CompanyURN  string     `json:"company_urn,omitempty"`
	Location    string     `json:"location,omitempty"`
	StartDate   YearMonth  `json:"start_date"`
	EndDate     *YearMonth `json:"end_date,omitempty"`
	Description string     `json:"description,omitempty"`
}

// Education is a single education entry.
type Education struct {
	School      string `json:"school"`
	Degree      string `json:"degree,omitempty"`
	Field       string `json:"field,omitempty"`
	StartYear   int    `json:"start_year,omitempty"`
	EndYear     int    `json:"end_year,omitempty"`
	Description string `json:"description,omitempty"`
}

// Skill is a LinkedIn skill with endorsement count.
type Skill struct {
	Name             string `json:"name"`
	EndorsementCount int    `json:"endorsement_count"`
}

// Certification is a LinkedIn certification entry.
type Certification struct {
	Name          string `json:"name"`
	Authority     string `json:"authority,omitempty"`
	LicenseNumber string `json:"license_number,omitempty"`
}

// ContactInfo contains profile contact information.
type ContactInfo struct {
	Email    string   `json:"email,omitempty"`
	Phone    string   `json:"phone,omitempty"`
	Twitter  string   `json:"twitter,omitempty"`
	Websites []string `json:"websites,omitempty"`
}

// Company is a LinkedIn company page.
type Company struct {
	URN           string   `json:"urn"`
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	Industry      string   `json:"industry"`
	Size          string   `json:"size"`
	Headquarters  string   `json:"headquarters"`
	Founded       int      `json:"founded,omitempty"`
	Specialties   []string `json:"specialties,omitempty"`
	Website       string   `json:"website,omitempty"`
	FollowerCount int      `json:"follower_count"`
	EmployeeCount int      `json:"employee_count"`
	JobCount      int      `json:"job_count"`
}

// Job is a LinkedIn job listing.
type Job struct {
	URN            string    `json:"urn"`
	Title          string    `json:"title"`
	Company        string    `json:"company"`
	CompanyURN     string    `json:"company_urn,omitempty"`
	Location       string    `json:"location"`
	Remote         string    `json:"remote"`
	PostedAt       time.Time `json:"posted_at"`
	Description    string    `json:"description"`
	Skills         []string  `json:"skills,omitempty"`
	SeniorityLevel string    `json:"seniority_level,omitempty"`
	EmploymentType string    `json:"employment_type,omitempty"`
	ApplyURL       string    `json:"apply_url,omitempty"`
}

// Connection is a profile's connection.
type Connection struct {
	ProfileURN  string    `json:"profile_urn"`
	Name        string    `json:"name"`
	Headline    string    `json:"headline"`
	ConnectedAt time.Time `json:"connected_at"`
}

// Post is a LinkedIn feed post.
type Post struct {
	URN         string    `json:"urn"`
	AuthorURN   string    `json:"author_urn"`
	Text        string    `json:"text"`
	MediaURLs   []string  `json:"media_urls,omitempty"`
	Likes       int       `json:"likes"`
	Comments    int       `json:"comments"`
	Reposts     int       `json:"reposts"`
	PublishedAt time.Time `json:"published_at"`
}

// ProfileRating is computed influence/quality metrics for a profile.
type ProfileRating struct {
	ConnectionCount     int     `json:"connection_count"`
	FollowerCount       int     `json:"follower_count"`
	PostFrequency       float64 `json:"post_frequency"`
	AvgEngagement       float64 `json:"avg_engagement"`
	TopEndorsedSkills   []Skill `json:"top_endorsed_skills"`
	RecommendationCount int     `json:"recommendation_count"`
	ProfileCompleteness int     `json:"profile_completeness"`
	InfluenceScore      float64 `json:"influence_score"`
}

// JobSearchParams are filters for SearchJobs.
type JobSearchParams struct {
	Query    string `json:"query"`
	Location string `json:"location,omitempty"`
	Remote   string `json:"remote,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

// SearchParams are filters for SearchPeople/SearchCompanies.
type SearchParams struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

// SearchResult is a single search result (person or company).
type SearchResult struct {
	URN      string `json:"urn"`
	Name     string `json:"name"`
	Headline string `json:"headline,omitempty"`
	Location string `json:"location,omitempty"`
	URL      string `json:"url,omitempty"`
}
