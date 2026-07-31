package jobs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/hunt"
)

// JobStatus represents the application status for a tracked job.
type JobStatus string

const (
	StatusSaved     JobStatus = "saved"
	StatusApplied   JobStatus = "applied"
	StatusInterview JobStatus = "interview"
	StatusOffer     JobStatus = "offer"
	StatusRejected  JobStatus = "rejected"
)

// TrackedJob is a single entry in the job tracker.
type TrackedJob struct {
	ID        int64     `json:"id"`
	Title     string    `json:"title"`
	Company   string    `json:"company"`
	URL       string    `json:"url"`
	Status    JobStatus `json:"status"`
	Notes     string    `json:"notes,omitempty"`
	Salary    string    `json:"salary,omitempty"`
	Location  string    `json:"location,omitempty"`
	CreatedAt string    `json:"created_at"`
	UpdatedAt string    `json:"updated_at"`
}

// JobTrackerAddInput is the input for job_tracker_add.
type JobTrackerAddInput struct {
	Title    string `json:"title"`
	Company  string `json:"company"`
	URL      string `json:"url,omitempty"`
	Status   string `json:"status,omitempty"`
	Notes    string `json:"notes,omitempty"`
	Salary   string `json:"salary,omitempty"`
	Location string `json:"location,omitempty"`
}

// JobTrackerListInput is the input for job_tracker_list.
type JobTrackerListInput struct {
	Status string `json:"status,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// JobTrackerUpdateInput is the input for job_tracker_update.
type JobTrackerUpdateInput struct {
	ID     int64  `json:"id"`
	Status string `json:"status,omitempty"`
	Notes  string `json:"notes,omitempty"`
}

// JobTrackerResult is the output for add/update operations.
type JobTrackerResult struct {
	ID      int64  `json:"id"`
	Message string `json:"message"`
}

// JobTrackerListResult is the output for list operations.
type JobTrackerListResult struct {
	Jobs  []TrackedJob `json:"jobs"`
	Total int          `json:"total"`
}

// trackerUser is the user_name used for all job_tracker ratings.
// Single-operator assumption per ADR-go-job-002.
const trackerUser = "krolik"

// validTrackerStatus validates the status/stage tokens the tracker tool accepts.
// These are a subset of hunt.Stage* constants (application pipeline).
func validTrackerStatus(s string) bool {
	switch s {
	case string(StatusSaved), string(StatusApplied), string(StatusInterview),
		string(StatusOffer), string(StatusRejected):
		return true
	}
	return false
}

// trackerDedup returns the dedup hash for a tracker entry.
// Primary: DedupHash(url). Fallback for URL-less entries: slugify(company+"-"+title).
func trackerDedup(url, company, title string) string {
	if url != "" {
		return hunt.DedupHash(url)
	}
	slug := strings.ToLower(strings.ReplaceAll(company+"-"+title, " ", "-"))
	return hunt.DedupHash(slug)
}

// formatSalary renders salary_min/max/currency/interval into the Salary string field.
//
// Format: "160000–220000 USD/year" (en dash, no "$" prefix, currency + interval
// suffix). This replaced the older "$160000-$220000 USD" shape when lever/ashby
// mappers started reusing this helper for the LLM-facing content string and
// FetchATSBoard compensation field. Nothing downstream splits the Salary string
// on "-" or "$": the scorer (hunt/score/scorer.go) renders SalaryMin/Max
// pointers directly into its prompt, qualityScoreFromListing reads the numeric
// pointers, and the Salary string is human-readable/LLM-facing only.
func formatSalary(min, max *int, currency, interval string) string {
	if min == nil && max == nil {
		return ""
	}
	var sb strings.Builder
	switch {
	case min != nil && max != nil && *min > 0 && *max > 0:
		fmt.Fprintf(&sb, "%d–%d", *min, *max)
	case min != nil && *min > 0:
		fmt.Fprintf(&sb, "%d+", *min)
	case max != nil && *max > 0:
		fmt.Fprintf(&sb, "up to %d", *max)
	}
	if sb.Len() == 0 {
		return ""
	}
	if currency != "" {
		fmt.Fprintf(&sb, " %s", currency)
	}
	if interval != "" {
		fmt.Fprintf(&sb, "/%s", interval)
	}
	return sb.String()
}

// trackerRate routes a status + note write to the correct axis of hunt_ratings
// (migration 012 split) using RateExact to guarantee single-observable-status
// coherence. RateExact unconditionally overwrites BOTH axes — the active axis
// gets the status value, the inactive axis is explicitly cleared to "".
//
// Without this explicit clear a prior triage='saved' row would survive a
// pipeline transition (applied/interview/offer/rejected) because Rate's CASE
// guard treats triage="" as PRESERVE; trackerStatusFromRow gives triage
// precedence → the pipeline state would be forever invisible.
//
//   - triage-axis:   status=="saved"  → RateExact(triage="saved", stage="", note)
//   - pipeline-axis: status∈pipeline  → RateExact(triage="",      stage=status, note)
func trackerRate(ctx context.Context, store *hunt.Store, id int64, status, note string) error {
	if status == hunt.StageSaved {
		return store.RateExact(ctx, hunt.KindJob, id, trackerUser, hunt.StageSaved, "", note)
	}
	return store.RateExact(ctx, hunt.KindJob, id, trackerUser, "", status, note)
}

// trackerStatusFromRow synthesises a single display status from the two-axis row.
// Triage takes precedence so "saved" is visible even when the job is also in the pipeline.
func trackerStatusFromRow(triage, stage string) JobStatus {
	if triage != "" {
		return JobStatus(triage)
	}
	return JobStatus(stage)
}

// AddTrackedJob saves a new job to the tracker via postgres.
func AddTrackedJob(ctx context.Context, input JobTrackerAddInput) (*JobTrackerResult, error) {
	if input.Title == "" || input.Company == "" {
		return nil, errors.New("job_tracker_add: title and company are required")
	}

	status := strings.ToLower(input.Status)
	if status == "" {
		status = string(StatusSaved)
	}
	if !validTrackerStatus(status) {
		return nil, fmt.Errorf("job_tracker_add: invalid status %q (valid: saved, applied, interview, offer, rejected)", status)
	}

	store := engine.GetHuntStore()
	if store == nil {
		return nil, errors.New("job_tracker_add: hunt store not available (DATABASE_URL not set?)")
	}

	j := hunt.Job{
		DedupHash: trackerDedup(input.URL, input.Company, input.Title),
		Title:     input.Title,
		Company:   input.Company,
		URL:       input.URL,
		Source:    "tracker",
		Location:  input.Location,
	}

	id, _, err := store.UpsertJob(ctx, j)
	if err != nil {
		return nil, fmt.Errorf("job_tracker_add: upsert job: %w", err)
	}

	note := input.Notes
	if input.Salary != "" {
		if note != "" {
			note += "; salary: " + input.Salary
		} else {
			note = "salary: " + input.Salary
		}
	}

	if err := trackerRate(ctx, store, id, status, note); err != nil {
		return nil, fmt.Errorf("job_tracker_add: rate: %w", err)
	}

	trackerOpsTotal.WithLabelValues("add", "pg").Inc()

	return &JobTrackerResult{
		ID:      id,
		Message: fmt.Sprintf("Job '%s' at '%s' saved with status '%s' (id=%d)", input.Title, input.Company, status, id),
	}, nil
}

// ListTrackedJobs returns tracked jobs, optionally filtered by status.
func ListTrackedJobs(ctx context.Context, input JobTrackerListInput) (*JobTrackerListResult, error) {
	store := engine.GetHuntStore()
	if store == nil {
		return &JobTrackerListResult{Jobs: []TrackedJob{}, Total: 0}, nil
	}

	status := ""
	if input.Status != "" {
		status = strings.ToLower(input.Status)
		if !validTrackerStatus(status) {
			return nil, fmt.Errorf("job_tracker_list: invalid status %q", status)
		}
	}

	rows, total, err := store.ListTrackedJobs(ctx, hunt.TrackedFilter{
		User:  trackerUser,
		Stage: status,
		Limit: input.Limit,
	})
	if err != nil {
		return nil, fmt.Errorf("job_tracker_list: %w", err)
	}

	jobs := make([]TrackedJob, 0, len(rows))
	for _, r := range rows {
		jobs = append(jobs, TrackedJob{
			ID:        r.ID,
			Title:     r.Title,
			Company:   r.Company,
			URL:       r.URL,
			Status:    trackerStatusFromRow(r.Triage, r.Stage),
			Notes:     r.Note,
			Salary:    formatSalary(r.SalaryMin, r.SalaryMax, r.SalaryCurrency, r.SalaryInterval),
			Location:  r.Location,
			CreatedAt: r.FirstSeenAt.UTC().Format(time.RFC3339),
			UpdatedAt: r.RatingUpdatedAt.UTC().Format(time.RFC3339),
		})
	}

	trackerOpsTotal.WithLabelValues("list", "pg").Inc()

	return &JobTrackerListResult{Jobs: jobs, Total: total}, nil
}

// UpdateTrackedJob updates the status and/or notes of a tracked job.
func UpdateTrackedJob(ctx context.Context, input JobTrackerUpdateInput) (*JobTrackerResult, error) {
	if input.ID <= 0 {
		return nil, errors.New("job_tracker_update: id is required")
	}
	if input.Status == "" && input.Notes == "" {
		return nil, errors.New("job_tracker_update: at least one of status or notes must be provided")
	}

	store := engine.GetHuntStore()
	if store == nil {
		return nil, errors.New("job_tracker_update: hunt store not available")
	}

	current, err := store.GetRating(ctx, hunt.KindJob, input.ID, trackerUser)
	if err != nil {
		if errors.Is(err, hunt.ErrNotFound) {
			if input.Status == "" {
				return nil, fmt.Errorf("job_tracker_update: job #%d has no rating yet; provide status to create one", input.ID)
			}
		} else {
			return nil, fmt.Errorf("job_tracker_update: get current rating: %w", err)
		}
	}

	newStatus := strings.ToLower(input.Status)
	note := input.Notes
	if current != nil {
		// Synthesize effective current status from both axes.
		if newStatus == "" {
			newStatus = string(trackerStatusFromRow(current.Triage, current.Stage))
		}
		if note == "" {
			note = current.Note
		}
	}

	if newStatus != "" && !validTrackerStatus(newStatus) {
		return nil, fmt.Errorf("job_tracker_update: invalid status %q", newStatus)
	}

	if err := trackerRate(ctx, store, input.ID, newStatus, note); err != nil {
		return nil, fmt.Errorf("job_tracker_update: %w", err)
	}

	trackerOpsTotal.WithLabelValues("update", "pg").Inc()

	return &JobTrackerResult{
		ID:      input.ID,
		Message: fmt.Sprintf("Job #%d updated successfully", input.ID),
	}, nil
}
