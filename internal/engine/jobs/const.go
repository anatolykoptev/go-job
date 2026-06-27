package jobs

// Source identifiers.
const (
	sourceAlgora    = "algora"
	sourceCode4rena = "code4rena"
	sourceHackerOne = "hackerone"
	sourceHabr      = "habr"
	sourceInspira   = "inspira"
	sourceLinkedIn  = "linkedin"
	sourceRemoteOK  = "remoteok"
)

// Status strings.
const (
	statusOpen      = "open"
	statusClosed    = "closed"
	statusCompleted = "completed"
)

// Opportunity / job types.
const (
	oppTypeAll       = "all"
	typeAuditContest = "audit_contest"
	jobTypeJob       = "job"
	jobTypeRemote    = "remote"
	locationRemote   = "Remote"
)

// Currency codes.
const currencyUSD = "USD"

// Graph node property keys.
const graphPropName = "name"

// Language alias keys (used in search alias maps).
const (
	langAliasGolang = "golang"
	langAliasRust   = "rust"
)

// Skill canonical names (used in skillPatterns).
const (
	skillRust   = "Rust"
	skillPython = "Python"
)

// Tone options.
const toneConcise = "concise"

// resumeVectorUser is the single-source user key for all resume_vectors rows.
// Single place per fitness function F3.
const resumeVectorUser = "gojob"

// Craigslist city slug.
const craigslistCityNewYork = "newyork"

// Additional source identifiers.
const (
	sourceUNDP          = "undp"
	sourceWeWorkRemotely = "weworkremotely"
	sourceSherlock      = "sherlock"
)

// Craigslist city slugs.
const craigslistCitySFBay = "sfbay"

// Metadata key used by job-source connectors (inspira, undp) in SearxngResult.Metadata.
const keySource = "source"

// mem_type discriminators for resume_vectors rows (one per consumer so ClearVectors
// and scoped searches cannot cross-contaminate between consumers).
const (
	memTypeResumeExp  = "resume_experience"
	memTypeResumeProj = "resume_project"
	memTypeResumeAchv = "resume_achievement"
	memTypeEnrichProj = "enrich_project"
)

// Tone options (additional).
const toneFriendly = "friendly"

// Contact/social metadata keys.
const contactKeyUsername = "username"
