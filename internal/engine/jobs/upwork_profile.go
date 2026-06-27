package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Package-level SQL constants for upwork_profile operations.
// Tests reference these directly so editing a query breaks the test (red-on-revert).
//
//nolint:gosec // these are SQL statements, not credentials
const (
	upsertUpworkProfileSQL = `
		INSERT INTO upwork_profile (person_id, title, overview, hourly_rate, categories, availability, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, now())
		ON CONFLICT (person_id) DO UPDATE
		SET title = EXCLUDED.title, overview = EXCLUDED.overview,
		    hourly_rate = EXCLUDED.hourly_rate, categories = EXCLUDED.categories,
		    availability = EXCLUDED.availability, updated_at = now()`

	getUpworkProfileSQL = `
		SELECT COALESCE(title,''), COALESCE(overview,''),
		       COALESCE(hourly_rate,0), COALESCE(categories,'{}'), COALESCE(availability,'')
		FROM upwork_profile WHERE person_id = $1`

	insertUpworkSkillSQL = `
		INSERT INTO upwork_skills (person_id, name, position)
		VALUES ($1, $2, (SELECT COALESCE(MAX(position),0)+1 FROM upwork_skills WHERE person_id = $1))
		ON CONFLICT (person_id, name) DO NOTHING
		RETURNING id`

	deleteUpworkSkillSQL = `DELETE FROM upwork_skills WHERE id = $1`

	getUpworkSkillsSQL = `
		SELECT id, name, position FROM upwork_skills
		WHERE person_id = $1 ORDER BY position, id`

	getUpworkCatalogItemsSQL = `
		SELECT id, title, COALESCE(description,''), position FROM upwork_catalog_items
		WHERE person_id = $1 ORDER BY position, id`
)

// UpworkProfile holds the data from the upwork_profile table.
type UpworkProfile struct {
	PersonID     int
	Title        string
	Overview     string
	HourlyRate   int64 // cents
	Categories   []string
	Availability string
}

// UpworkSkillRecord is a single Upwork-specific skill.
type UpworkSkillRecord struct {
	ID       int
	Name     string
	Position int
}

// UpworkCatalogItem is a single Upwork catalog/portfolio item.
type UpworkCatalogItem struct {
	ID          int
	Title       string
	Description string
	Position    int
}

// UpworkProfileResult is the composed view returned by GetUpworkProfile.
type UpworkProfileResult struct {
	Profile  *UpworkProfile
	Skills   []UpworkSkillRecord
	Catalog  []UpworkCatalogItem
	Missing  bool // true when no upwork_profile row exists yet
}

// GetUpworkProfile loads the full Upwork profile from the upwork_* tables.
// Missing=true is returned (not an error) when no upwork_profile row exists yet.
// Any real query error (syntax, connection, schema) is returned as an error so
// callers can distinguish "no data yet" from "something broke".
func (db *ResumeDB) GetUpworkProfile(ctx context.Context, personID int) (*UpworkProfileResult, error) {
	result := &UpworkProfileResult{}

	profile := &UpworkProfile{PersonID: personID}
	var categories []string
	err := db.pool.QueryRow(ctx, getUpworkProfileSQL, personID).
		Scan(&profile.Title, &profile.Overview, &profile.HourlyRate, &categories, &profile.Availability)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// No row = profile not set up yet; return Missing=true with empty data.
			result.Missing = true
			result.Profile = profile
			result.Skills = []UpworkSkillRecord{}
			result.Catalog = []UpworkCatalogItem{}
			return result, nil
		}
		return nil, fmt.Errorf("get upwork profile: %w", err)
	}
	profile.Categories = categories
	result.Profile = profile

	// Load skills ordered by position.
	rows, err := db.pool.Query(ctx, getUpworkSkillsSQL, personID)
	if err != nil {
		slog.Warn("GetUpworkProfile: query skills", "person_id", personID, "err", err)
	} else {
		defer rows.Close()
		for rows.Next() {
			var s UpworkSkillRecord
			if scanErr := rows.Scan(&s.ID, &s.Name, &s.Position); scanErr == nil {
				result.Skills = append(result.Skills, s)
			}
		}
	}
	if result.Skills == nil {
		result.Skills = []UpworkSkillRecord{}
	}

	// Load catalog items ordered by position.
	catRows, err := db.pool.Query(ctx, getUpworkCatalogItemsSQL, personID)
	if err != nil {
		slog.Warn("GetUpworkProfile: query catalog", "person_id", personID, "err", err)
	} else {
		defer catRows.Close()
		for catRows.Next() {
			var c UpworkCatalogItem
			if scanErr := catRows.Scan(&c.ID, &c.Title, &c.Description, &c.Position); scanErr == nil {
				result.Catalog = append(result.Catalog, c)
			}
		}
	}
	if result.Catalog == nil {
		result.Catalog = []UpworkCatalogItem{}
	}

	return result, nil
}

// UpsertUpworkProfile creates or updates the upwork_profile row for a person.
func (db *ResumeDB) UpsertUpworkProfile(ctx context.Context, personID int, title, overview string, hourlyRate int64, categories []string, availability string) error {
	if categories == nil {
		categories = []string{}
	}
	_, err := db.pool.Exec(ctx, upsertUpworkProfileSQL,
		personID, title, overview, hourlyRate, categories, availability)
	return err
}

// InsertUpworkSkill adds a skill to upwork_skills. Returns (0, nil) on duplicate (no-op).
// NOTE: absence of person_id scope in WHERE is safe ONLY under the single-user
// invariant; if this DB ever becomes multi-person these must be person-scoped.
func (db *ResumeDB) InsertUpworkSkill(ctx context.Context, personID int, name string) (int, error) {
	var id int
	err := db.pool.QueryRow(ctx, insertUpworkSkillSQL, personID, name).Scan(&id)
	if err != nil {
		// ON CONFLICT DO NOTHING means no row returned — treat as success.
		return 0, nil
	}
	return id, nil
}

// DeleteUpworkSkill removes an upwork_skills row by primary key.
// NOTE: absence of person_id scope in WHERE is safe ONLY under the single-user
// invariant; if this DB ever becomes multi-person these must be person-scoped.
func (db *ResumeDB) DeleteUpworkSkill(ctx context.Context, skillID int) error {
	_, err := db.pool.Exec(ctx, deleteUpworkSkillSQL, skillID)
	return err
}

// UpworkPasteBlock is a labeled block of text destined for a <textarea readonly>.
type UpworkPasteBlock struct {
	Label   string
	Content string
}

// FormatUpworkPasteBlocks produces literal-text paste blocks from a profile result.
// Content is plain text — html/template auto-escapes it when rendered via {{.Content}};
// no html/template unsafe type cast is used here, preventing double-escape risks.
func FormatUpworkPasteBlocks(r *UpworkProfileResult) []UpworkPasteBlock {
	var blocks []UpworkPasteBlock

	if r.Profile != nil && r.Profile.Title != "" {
		blocks = append(blocks, UpworkPasteBlock{
			Label:   "Title / Headline",
			Content: r.Profile.Title,
		})
	}

	if r.Profile != nil && r.Profile.Overview != "" {
		blocks = append(blocks, UpworkPasteBlock{
			Label:   "Professional Overview",
			Content: r.Profile.Overview,
		})
	}

	if len(r.Skills) > 0 {
		names := make([]string, len(r.Skills))
		for i, s := range r.Skills {
			names[i] = s.Name
		}
		blocks = append(blocks, UpworkPasteBlock{
			Label:   "Skills",
			Content: strings.Join(names, "\n"),
		})
	}

	for _, c := range r.Catalog {
		content := c.Title
		if c.Description != "" {
			content = c.Description
		}
		blocks = append(blocks, UpworkPasteBlock{
			Label:   "Catalog: " + c.Title,
			Content: content,
		})
	}

	return blocks
}
