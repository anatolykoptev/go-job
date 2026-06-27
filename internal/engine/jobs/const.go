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

// Graph node type tags.
const graphTypeProject = "project"

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

// MemDB user identity for go-job.
const memdbUserID = "gojob"

// resumeVectorUser is the single-source cube key for all resume_vectors rows.
// Matches memdbUserID so the Phase-A migration can use the same value when pulling
// from MemDB (cmd/migrate-resume-memory). Single place per fitness function F3.
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

// MemDB metadata keys.
const (
	memdbKeyUserID = "user_id"
	memdbKeyType   = "type"
	memdbKeySource = "source"
)

// Tone options (additional).
const toneFriendly = "friendly"

// Contact/social metadata keys.
const contactKeyUsername = "username"
