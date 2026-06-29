// Package hunt provides domain-typed persistent storage for job-hunting search
// results with URL-hash deduplication. Each search-tool writes typed rows; repeated
// searches deduplicate by canonical URL hash and bump last_seen_at instead of
// re-emitting identical entries.
package hunt

import (
	"encoding/json"
	"time"
)

// Stage values for hunt_ratings.stage. Checked in Go, not via SQL CHECK constraint
// to keep the schema flexible for future stages without migrations.
//
// Triage stages (kanban):
const (
	StageNew         = "new"
	StageInteresting = "interesting"
	StageSaved       = "saved"
	StageDiscarded   = "discarded"
	StageClaimed     = "claimed"
)

// Pipeline stages (application funnel) — added in Phase 1 unification arc
// (ADR-go-job-002). No new columns; stage has no SQL CHECK constraint.
const (
	StageApplied   = "applied"
	StageInterview = "interview"
	StageOffer     = "offer"
	StageRejected  = "rejected"
)

// TriageStages are operator-assessment phases before application.
// Ordered: earliest decision first.
var TriageStages = []string{StageNew, StageInteresting, StageSaved, StageDiscarded, StageClaimed}

// PipelineStages are post-application funnel phases.
// Ordered: earliest funnel step first.
var PipelineStages = []string{StageApplied, StageInterview, StageOffer, StageRejected}

// AllStages is the canonical ordered list of hunt stages (triage + pipeline).
// Adding a new stage: edit TriageStages or PipelineStages — AllStages is derived automatically.
// Order matters for UI presentation (triage first, then funnel stages).
var AllStages = append(append([]string{}, TriageStages...), PipelineStages...)

// Status values for hunt entry lifecycle. Default is "open".
// "closed" — issue closed without merge; "merged" — PR merged / bounty claimed;
// "archived" — repo/program archived; "ended" — time-bound contest expired.
const (
	StatusOpen     = "open"
	StatusClosed   = "closed"
	StatusMerged   = "merged"
	StatusArchived = "archived"
	StatusEnded    = "ended"
)

// AllStatuses is the canonical ordered list of hunt_jobs status values.
// Adding a new status: edit ONLY this slice; everything else derives from it.
var AllStatuses = []string{
	StatusOpen, StatusClosed, StatusMerged, StatusArchived, StatusEnded,
}

// Kind values identify which hunt table an entry_id refers to.
const (
	KindBounty       = "bounty"
	KindJob          = "job"
	KindFreelance    = "freelance"
	KindSecurity     = "security"
	KindAuditContest = "audit_contest"
)

// Bounty is a persistent open-source bounty record (widest projection of BountyListing).
type Bounty struct {
	ID            int64           `json:"id"`
	DedupHash     string          `json:"dedup_hash"`
	Title         string          `json:"title"`
	URL           string          `json:"url"`
	Org           string          `json:"org,omitempty"`
	Source        string          `json:"source"`
	AmountCents   int64           `json:"amount_cents,omitempty"`
	Currency      string          `json:"currency,omitempty"`
	IssueNumber   int             `json:"issue_number,omitempty"`
	Skills        []string        `json:"skills,omitempty"`
	Description   string          `json:"description,omitempty"`
	Relevance     float32         `json:"relevance,omitempty"`
	Status        string          `json:"status"` // "open" / "closed" / "merged" / "archived"
	ClosedAt      *time.Time      `json:"closed_at,omitempty"`
	LastCheckedAt *time.Time      `json:"last_checked_at,omitempty"`
	PostedAt      *time.Time      `json:"posted_at,omitempty"`
	FirstSeenAt   time.Time       `json:"first_seen_at"`
	LastSeenAt    time.Time       `json:"last_seen_at"`
	Raw           json.RawMessage `json:"raw,omitempty"`
}

// Job is a persistent job listing record (widest projection of JobListing).
type Job struct {
	ID             int64           `json:"id"`
	DedupHash      string          `json:"dedup_hash"`
	Title          string          `json:"title"`
	Company        string          `json:"company,omitempty"`
	URL            string          `json:"url"`
	Source         string          `json:"source"`
	ExternalID     string          `json:"external_id,omitempty"`
	Location       string          `json:"location,omitempty"`
	Remote         string          `json:"remote,omitempty"`
	JobType        string          `json:"job_type,omitempty"`
	Experience     string          `json:"experience,omitempty"`
	SalaryMin      int             `json:"salary_min,omitempty"`
	SalaryMax      int             `json:"salary_max,omitempty"`
	SalaryCurrency string          `json:"salary_currency,omitempty"`
	SalaryInterval string          `json:"salary_interval,omitempty"`
	Skills         []string        `json:"skills,omitempty"`
	Tags           []string        `json:"tags,omitempty"`
	Description    string          `json:"description,omitempty"`
	Status         string          `json:"status"` // "open" / "closed" / "merged" / "archived"
	ClosedAt       *time.Time      `json:"closed_at,omitempty"`
	LastCheckedAt  *time.Time      `json:"last_checked_at,omitempty"`
	PostedAt       *time.Time      `json:"posted_at,omitempty"`
	FirstSeenAt    time.Time       `json:"first_seen_at"`
	LastSeenAt     time.Time       `json:"last_seen_at"`
	Raw            json.RawMessage `json:"raw,omitempty"`
}

// Freelance is a persistent freelance project record (widest projection of FreelanceProject/FreelanceJob).
type Freelance struct {
	ID             int64           `json:"id"`
	DedupHash      string          `json:"dedup_hash"`
	Title          string          `json:"title"`
	URL            string          `json:"url"`
	Platform       string          `json:"platform"`
	Source         string          `json:"source"`
	BudgetMin      int             `json:"budget_min,omitempty"`
	BudgetMax      int             `json:"budget_max,omitempty"`
	BudgetCurrency string          `json:"budget_currency,omitempty"`
	BudgetRaw      string          `json:"budget_raw,omitempty"`
	Location       string          `json:"location,omitempty"`
	Skills         []string        `json:"skills,omitempty"`
	Tags           []string        `json:"tags,omitempty"`
	Description    string          `json:"description,omitempty"`
	ClientInfo     string          `json:"client_info,omitempty"`
	Status         string          `json:"status"` // "open" / "closed" / "archived"
	ClosedAt       *time.Time      `json:"closed_at,omitempty"`
	LastCheckedAt  *time.Time      `json:"last_checked_at,omitempty"`
	PostedAt       *time.Time      `json:"posted_at,omitempty"`
	FirstSeenAt    time.Time       `json:"first_seen_at"`
	LastSeenAt     time.Time       `json:"last_seen_at"`
	Raw            json.RawMessage `json:"raw,omitempty"`
}

// Security is a persistent bug-bounty / security program record.
type Security struct {
	ID            int64           `json:"id"`
	DedupHash     string          `json:"dedup_hash"`
	Name          string          `json:"name"`
	URL           string          `json:"url"`
	Platform      string          `json:"platform"`
	ProgramType   string          `json:"program_type,omitempty"`
	MinBounty     int             `json:"min_bounty,omitempty"`
	MaxBounty     int             `json:"max_bounty,omitempty"`
	Targets       []string        `json:"targets,omitempty"`
	Managed       bool            `json:"managed"`
	Description   string          `json:"description,omitempty"`
	Status        string          `json:"status"` // "open" / "closed" / "archived"
	ClosedAt      *time.Time      `json:"closed_at,omitempty"`
	LastCheckedAt *time.Time      `json:"last_checked_at,omitempty"`
	FirstSeenAt   time.Time       `json:"first_seen_at"`
	LastSeenAt    time.Time       `json:"last_seen_at"`
	Raw           json.RawMessage `json:"raw,omitempty"`
}

// AuditContest is a persistent smart-contract audit contest record.
type AuditContest struct {
	ID          int64           `json:"id"`
	DedupHash   string          `json:"dedup_hash"`
	Title       string          `json:"title"`
	URL         string          `json:"url"`
	Platform    string          `json:"platform"`
	TotalPool   int             `json:"total_pool,omitempty"`
	Currency    string          `json:"currency,omitempty"`
	StartsAt    *time.Time      `json:"starts_at,omitempty"`
	EndsAt      *time.Time      `json:"ends_at,omitempty"`
	Languages   []string        `json:"languages,omitempty"`
	Description string          `json:"description,omitempty"`
	FirstSeenAt time.Time       `json:"first_seen_at"`
	LastSeenAt  time.Time       `json:"last_seen_at"`
	Raw         json.RawMessage `json:"raw,omitempty"`
}

// ScoreResult carries the LLM fit-scoring output for a single job.
// The FitReasons, FitGaps, and SuccessReasoning fields are marshalled
// together into the score_rationale JSONB column as:
//
//	{"fit_reasons":["..."],"fit_gaps":["..."],"success_reasoning":"..."}
//
// FitScore is the 0-100 fit alignment score (Axis 1).
// FitBand is the display band derived from FitScore ("strong"/"moderate"/"low"/
// "reject"), or the FitBandUnscored sentinel when the LLM scorer failed.
// SuccessBand is one of "STRONG"/"MODERATE"/"LONGSHOT".
// OverUnder is one of "under_qualified"/"well_matched"/"over_qualified".
// LLMCalled is true only when the full LLM branch was reached (i.e. the job
// was fresh, above the Jaccard threshold, and the LLM was actually invoked).
// It is false for stale/nil-profile/sub-Jaccard short-circuits. The per-cycle
// circuit-breaker counter MUST use this flag rather than counting all jobs
// processed, so stale/rejected jobs do not exhaust the real LLM budget.
// FitBandUnscored is the sentinel FitBand for a degraded score: the LLM scorer
// failed (parse error / proxy down) so the job is notified fail-open with a
// recency-only card. The worker, notifier, and scorer all key on this value to
// select the degraded render path and the post-recency "unscored" metric; it is
// distinct from the numeric display bands ("strong"/"moderate"/"low"/"reject").
const FitBandUnscored = "unscored"

// FitBandStale and FitBandReject are the pre-LLM short-circuit bands.
// FitBandStale: job PostedAt nil or older than HUNT_NOTIFY_MAX_AGE.
// FitBandReject: job failed the Jaccard keyword-overlap pre-filter.
// Both are keyed by observeScore (worker) and the scorer to route the
// hunt_score_filtered_total metric. Centralised here so a rename cannot
// silently diverge scorer.go from worker.go.
const (
	FitBandStale  = "stale"
	FitBandReject = "reject"
)

type ScoreResult struct {
	FitScore         int       `json:"fit_score"`
	FitBand          string    `json:"fit_band"`
	SuccessBand      string    `json:"success_band"`
	OverUnder        string    `json:"over_under"`
	FitReasons       []string  `json:"fit_reasons"`
	FitGaps          []string  `json:"fit_gaps"`
	SuccessReasoning string    `json:"success_reasoning"`
	ScoredAt         time.Time `json:"scored_at"`
	// LLMCalled is not persisted to the DB (no JSON tag) — it is a transient
	// signal for the circuit-breaker in huntworker.
	LLMCalled bool `json:"-"`
	// LLMResult is a transient signal (not persisted) for the scorer-outcome metric.
	// One of "ok" | "enum_clamp" | "parse_fail" | "llm_error"; empty for pre-LLM
	// short-circuits (stale/reject) which are counted via FitBand + the filter metric.
	LLMResult string `json:"-"`
}

// scoreRationale is the JSON shape stored in the score_rationale JSONB column.
type scoreRationale struct {
	FitReasons       []string `json:"fit_reasons"`
	FitGaps          []string `json:"fit_gaps"`
	SuccessReasoning string   `json:"success_reasoning"`
}

// Rating is a per-user kanban rating for any hunt entry.
type Rating struct {
	ID        int64     `json:"id"`
	EntryKind string    `json:"entry_kind"`
	EntryID   int64     `json:"entry_id"`
	UserName  string    `json:"user_name"`
	Stage     string    `json:"stage"`
	Note      string    `json:"note,omitempty"`
	RatedAt   time.Time `json:"rated_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SourceCount is a per-source open job count returned by Store.CountBySource.
type SourceCount struct {
	Source string
	N      int
}
