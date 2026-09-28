package jobserver

import "errors"

// errNoAccountIdentity fails a tool call closed when the request context
// carries neither a verified web-session account nor an MCP bearer-token
// account (plan ADR-1). Account-owned data must never resolve to a global or
// fallback owner.
var errNoAccountIdentity = errors.New("account identity required")

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
