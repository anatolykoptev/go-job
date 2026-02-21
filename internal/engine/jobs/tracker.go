package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
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

var (
	trackerDB   *sql.DB
	trackerOnce sync.Once
	trackerErr  error
)

// openTrackerDB opens (or creates) the SQLite tracker database.
func openTrackerDB() (*sql.DB, error) {
	trackerOnce.Do(func() {
		dir := filepath.Join(os.Getenv("HOME"), ".go_job")
		if err := os.MkdirAll(dir, 0750); err != nil {
			trackerErr = fmt.Errorf("tracker: mkdir %s: %w", dir, err)
			return
		}
		dbPath := filepath.Join(dir, "tracker.db")
		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			trackerErr = fmt.Errorf("tracker: open db: %w", err)
			return
		}
		db.SetMaxOpenConns(1) // SQLite: single writer
		if err := initTrackerSchema(db); err != nil {
			trackerErr = fmt.Errorf("tracker: init schema: %w", err)
			return
		}
		trackerDB = db
	})
	return trackerDB, trackerErr
}

// initTrackerSchema creates the jobs table if it doesn't exist.
func initTrackerSchema(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS jobs (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		title      TEXT NOT NULL,
		company    TEXT NOT NULL,
		url        TEXT,
		status     TEXT NOT NULL DEFAULT 'saved',
		notes      TEXT,
		salary     TEXT,
		location   TEXT,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`)
	return err
}

// validStatus checks if a status string is valid.
func validStatus(s string) bool {
	switch JobStatus(s) {
	case StatusSaved, StatusApplied, StatusInterview, StatusOffer, StatusRejected:
		return true
	}
	return false
}

// AddTrackedJob saves a new job to the tracker.
func AddTrackedJob(_ context.Context, input JobTrackerAddInput) (*JobTrackerResult, error) {
	if input.Title == "" || input.Company == "" {
		return nil, errors.New("job_tracker_add: title and company are required")
	}

	status := strings.ToLower(input.Status)
	if status == "" {
		status = string(StatusSaved)
	}
	if !validStatus(status) {
		return nil, fmt.Errorf("job_tracker_add: invalid status %q (valid: saved, applied, interview, offer, rejected)", status)
	}

	db, err := openTrackerDB()
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	res, err := db.Exec(
		`INSERT INTO jobs (title, company, url, status, notes, salary, location, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		input.Title, input.Company, input.URL, status,
		input.Notes, input.Salary, input.Location, now, now,
	)
	if err != nil {
		return nil, fmt.Errorf("job_tracker_add: insert: %w", err)
	}

	id, _ := res.LastInsertId()
	return &JobTrackerResult{
		ID:      id,
		Message: fmt.Sprintf("Job '%s' at '%s' saved with status '%s' (id=%d)", input.Title, input.Company, status, id),
	}, nil
}

// ListTrackedJobs returns tracked jobs, optionally filtered by status.
func ListTrackedJobs(_ context.Context, input JobTrackerListInput) (*JobTrackerListResult, error) {
	db, err := openTrackerDB()
	if err != nil {
		return nil, err
	}

	limit := input.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	var rows *sql.Rows
	if input.Status != "" {
		status := strings.ToLower(input.Status)
		if !validStatus(status) {
			return nil, fmt.Errorf("job_tracker_list: invalid status %q", status)
		}
		rows, err = db.Query(
			`SELECT id, title, company, url, status, notes, salary, location, created_at, updated_at
			 FROM jobs WHERE status = ? ORDER BY updated_at DESC LIMIT ?`,
			status, limit,
		)
	} else {
		rows, err = db.Query(
			`SELECT id, title, company, url, status, notes, salary, location, created_at, updated_at
			 FROM jobs ORDER BY updated_at DESC LIMIT ?`,
			limit,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("job_tracker_list: query: %w", err)
	}
	defer rows.Close()

	var jobs []TrackedJob
	for rows.Next() {
		var j TrackedJob
		var notes, salary, location, url sql.NullString
		if err := rows.Scan(&j.ID, &j.Title, &j.Company, &url, &j.Status,
			&notes, &salary, &location, &j.CreatedAt, &j.UpdatedAt); err != nil {
			continue
		}
		j.URL = url.String
		j.Notes = notes.String
		j.Salary = salary.String
		j.Location = location.String
		jobs = append(jobs, j)
	}

	// Count total matching rows
	var total int
	if input.Status != "" {
		db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE status = ?`, strings.ToLower(input.Status)).Scan(&total) //nolint:errcheck
	} else {
		db.QueryRow(`SELECT COUNT(*) FROM jobs`).Scan(&total) //nolint:errcheck
	}

	if jobs == nil {
		jobs = []TrackedJob{}
	}
	return &JobTrackerListResult{Jobs: jobs, Total: total}, nil
}

// UpdateTrackedJob updates the status and/or notes of a tracked job.
func UpdateTrackedJob(_ context.Context, input JobTrackerUpdateInput) (*JobTrackerResult, error) {
	if input.ID <= 0 {
		return nil, errors.New("job_tracker_update: id is required")
	}
	if input.Status == "" && input.Notes == "" {
		return nil, errors.New("job_tracker_update: at least one of status or notes must be provided")
	}

	db, err := openTrackerDB()
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC().Format(time.RFC3339)

	switch {
	case input.Status != "" && input.Notes != "":
		status := strings.ToLower(input.Status)
		if !validStatus(status) {
			return nil, fmt.Errorf("job_tracker_update: invalid status %q", status)
		}
		_, err = db.Exec(`UPDATE jobs SET status=?, notes=?, updated_at=? WHERE id=?`,
			status, input.Notes, now, input.ID)
	case input.Status != "":
		status := strings.ToLower(input.Status)
		if !validStatus(status) {
			return nil, fmt.Errorf("job_tracker_update: invalid status %q", status)
		}
		_, err = db.Exec(`UPDATE jobs SET status=?, updated_at=? WHERE id=?`,
			status, now, input.ID)
	default:
		_, err = db.Exec(`UPDATE jobs SET notes=?, updated_at=? WHERE id=?`,
			input.Notes, now, input.ID)
	}

	if err != nil {
		return nil, fmt.Errorf("job_tracker_update: %w", err)
	}

	return &JobTrackerResult{
		ID:      input.ID,
		Message: fmt.Sprintf("Job #%d updated successfully", input.ID),
	}, nil
}
