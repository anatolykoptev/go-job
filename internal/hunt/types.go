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
const (
	StageNew         = "new"
	StageInteresting = "interesting"
	StageSaved       = "saved"
	StageDiscarded   = "discarded"
	StageClaimed     = "claimed"
)

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
	ID          int64           `json:"id"`
	DedupHash   string          `json:"dedup_hash"`
	Title       string          `json:"title"`
	URL         string          `json:"url"`
	Org         string          `json:"org,omitempty"`
	Source      string          `json:"source"`
	AmountCents int64           `json:"amount_cents,omitempty"`
	Currency    string          `json:"currency,omitempty"`
	IssueNumber int             `json:"issue_number,omitempty"`
	Skills      []string        `json:"skills,omitempty"`
	Description string          `json:"description,omitempty"`
	Relevance   float32         `json:"relevance,omitempty"`
	PostedAt    *time.Time      `json:"posted_at,omitempty"`
	FirstSeenAt time.Time       `json:"first_seen_at"`
	LastSeenAt  time.Time       `json:"last_seen_at"`
	Raw         json.RawMessage `json:"raw,omitempty"`
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
	PostedAt       *time.Time      `json:"posted_at,omitempty"`
	FirstSeenAt    time.Time       `json:"first_seen_at"`
	LastSeenAt     time.Time       `json:"last_seen_at"`
	Raw            json.RawMessage `json:"raw,omitempty"`
}

// Security is a persistent bug-bounty / security program record.
type Security struct {
	ID          int64           `json:"id"`
	DedupHash   string          `json:"dedup_hash"`
	Name        string          `json:"name"`
	URL         string          `json:"url"`
	Platform    string          `json:"platform"`
	ProgramType string          `json:"program_type,omitempty"`
	MinBounty   int             `json:"min_bounty,omitempty"`
	MaxBounty   int             `json:"max_bounty,omitempty"`
	Targets     []string        `json:"targets,omitempty"`
	Managed     bool            `json:"managed"`
	Description string          `json:"description,omitempty"`
	FirstSeenAt time.Time       `json:"first_seen_at"`
	LastSeenAt  time.Time       `json:"last_seen_at"`
	Raw         json.RawMessage `json:"raw,omitempty"`
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
