package jobserver

// Opportunity type strings (mirror jobs package unexported constants).
const (
	oppTypeBounty    = "bounty"
	oppTypeSecurity  = "security"
	oppTypeFreelance = "freelance"
)

// Opportunity verdict strings.
const verdictManual = "manual"

// Hunt kind strings for hunt_list tool.
const (
	huntKindJobs      = "jobs"
	huntKindBounties  = "bounties"
	huntKindFreelance = "freelance"
	huntKindSecurity  = "security"
)

// LinkedIn op strings for linkedin tool.
const (
	linkedInOpProfile = "profile"
	linkedInOpCompany = "company"
	linkedInOpPosts   = "posts"
	linkedInOpRating  = "rating"
	linkedInOpSearch  = "search"
	linkedInOpJobs    = "jobs"
)

// Map key strings used in inline map literals.
const keyType = "type"
