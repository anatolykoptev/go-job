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
		SELECT $1, $2, $3, $4, $5, $6, now()
		WHERE EXISTS (SELECT 1 FROM resume_persons WHERE id = $1 AND account_id = $7)
		ON CONFLICT (person_id) DO UPDATE
		SET title = EXCLUDED.title, overview = EXCLUDED.overview,
		    hourly_rate = EXCLUDED.hourly_rate, categories = EXCLUDED.categories,
		    availability = EXCLUDED.availability, updated_at = now()`

	getUpworkProfileSQL = `
		SELECT COALESCE(title,''), COALESCE(overview,''),
		       COALESCE(hourly_rate,0), COALESCE(categories,'{}'), COALESCE(availability,'')
		FROM upwork_profile WHERE person_id = $1
		  AND EXISTS (SELECT 1 FROM resume_persons WHERE id = $1 AND account_id = $2)`

	insertUpworkSkillSQL = `
		INSERT INTO upwork_skills (person_id, name, position)
		SELECT $1, $2, (SELECT COALESCE(MAX(position),0)+1 FROM upwork_skills WHERE person_id = $1)
		WHERE EXISTS (SELECT 1 FROM resume_persons WHERE id = $1 AND account_id = $3)
		ON CONFLICT (person_id, name) DO NOTHING
		RETURNING id`

	getUpworkSkillsSQL = `
		SELECT id, name, position FROM upwork_skills
		WHERE person_id = $1
		  AND EXISTS (SELECT 1 FROM resume_persons WHERE id = $1 AND account_id = $2)
		ORDER BY position, id`

	getUpworkCatalogItemsSQL = `
		SELECT id, title, COALESCE(description,''), position FROM upwork_catalog_items
		WHERE person_id = $1
		  AND EXISTS (SELECT 1 FROM resume_persons WHERE id = $1 AND account_id = $2)
		ORDER BY position, id`
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
	Profile *UpworkProfile
	Skills  []UpworkSkillRecord
	Catalog []UpworkCatalogItem
	Missing bool // true when no upwork_profile row exists yet
}

// GetUpworkProfile loads the full Upwork profile from the upwork_* tables.
// Missing=true is returned (not an error) when no upwork_profile row exists yet.
// Any real query error (syntax, connection, schema) is returned as an error so
// callers can distinguish "no data yet" from "something broke".
func (a *ResumeAccount) GetUpworkProfile(ctx context.Context, personID int) (*UpworkProfileResult, error) {
	result := &UpworkProfileResult{}

	profile := &UpworkProfile{PersonID: personID}
	var categories []string
	err := a.db.pool.QueryRow(ctx, getUpworkProfileSQL, personID, a.aid).
		Scan(&profile.Title, &profile.Overview, &profile.HourlyRate, &categories, &profile.Availability)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// No upwork_profile row yet. Skills and catalog items can exist
			// independently of the profile row, so mark Missing and fall through
			// to load them rather than returning early with empty lists.
			result.Missing = true
			result.Profile = profile
		} else {
			return nil, fmt.Errorf("get upwork profile: %w", err)
		}
	} else {
		profile.Categories = categories
		result.Profile = profile
	}

	// Load skills ordered by position.
	rows, err := a.db.pool.Query(ctx, getUpworkSkillsSQL, personID, a.aid)
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
	catRows, err := a.db.pool.Query(ctx, getUpworkCatalogItemsSQL, personID, a.aid)
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
func (a *ResumeAccount) UpsertUpworkProfile(ctx context.Context, personID int, title, overview string, hourlyRate int64, categories []string, availability string) error {
	if !a.writable() {
		return ErrNoAccountScope
	}

	if categories == nil {
		categories = []string{}
	}
	_, err := a.db.pool.Exec(ctx, upsertUpworkProfileSQL,
		personID, title, overview, hourlyRate, categories, availability, a.aid)
	return err
}

// InsertUpworkSkill adds a skill to upwork_skills.
// Returns (0, nil) when the skill already exists (ON CONFLICT DO NOTHING — no row returned).
// Any other error (FK violation, dead pool, context cancellation) is propagated.
// NOTE: absence of person_id scope in WHERE is safe ONLY under the single-user
// invariant; if this DB ever becomes multi-person these must be person-scoped.
func (a *ResumeAccount) InsertUpworkSkill(ctx context.Context, personID int, name string) (int, error) {
	if !a.writable() {
		return 0, ErrNoAccountScope
	}

	var id int
	err := a.db.pool.QueryRow(ctx, insertUpworkSkillSQL, personID, name, a.aid).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Genuine ON CONFLICT DO NOTHING — duplicate skill, treat as success.
			return 0, nil
		}
		return 0, fmt.Errorf("insert upwork skill: %w", err)
	}
	return id, nil
}

// DeleteUpworkSkill removes an upwork_skills row by primary key, scoped to the given person.
// The WHERE clause includes person_id to prevent cross-person deletion.
func (a *ResumeAccount) DeleteUpworkSkill(ctx context.Context, personID, id int) error {
	if !a.writable() {
		return ErrNoAccountScope
	}

	_, err := a.db.pool.Exec(ctx, deleteUpworkSkillPersonSQL, id, personID, a.aid)
	return err
}

// GetUpworkSkillByID fetches a single upwork_skills row by primary key.
func (a *ResumeAccount) GetUpworkSkillByID(ctx context.Context, skillID int) (UpworkSkillRecord, error) {
	var s UpworkSkillRecord
	err := a.conn(ctx).QueryRow(ctx,
		`SELECT id, name, position FROM upwork_skills WHERE id = $1 AND `+personOwnedBy2, skillID, a.aid).
		Scan(&s.ID, &s.Name, &s.Position)
	return s, err
}

// UpdateUpworkSkill updates the name of an upwork_skills row.
func (a *ResumeAccount) UpdateUpworkSkill(ctx context.Context, skillID int, name string) error {
	if !a.writable() {
		return ErrNoAccountScope
	}

	_, err := a.conn(ctx).Exec(ctx,
		`UPDATE upwork_skills SET name = $2 WHERE id = $1 AND `+personOwnedBy3, skillID, name, a.aid)
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

// New SQL constants for catalog CRUD + reorder (all person-scoped per ADR #7)
//
//nolint:gosec // these are SQL statements, not credentials
const (
	insertUpworkCatalogItemSQL = `
		INSERT INTO upwork_catalog_items (person_id, title, description, position)
		SELECT $1, $2, $3,
		       (SELECT COALESCE(MAX(position), 0) + 1
		        FROM upwork_catalog_items WHERE person_id = $1)
		WHERE EXISTS (SELECT 1 FROM resume_persons WHERE id = $1 AND account_id = $4)
		RETURNING id`

	deleteUpworkCatalogItemSQL = `
		DELETE FROM upwork_catalog_items
		WHERE id = $1 AND person_id = $2 AND person_id IN (SELECT id FROM resume_persons WHERE account_id = $3)`

	deleteUpworkSkillPersonSQL = `
		DELETE FROM upwork_skills
		WHERE id = $1 AND person_id = $2 AND person_id IN (SELECT id FROM resume_persons WHERE account_id = $3)`
)

// InsertUpworkCatalogItem adds a new catalog item to upwork_catalog_items.
// Position is seeded as MAX(position)+1 per person.
// Returns the new item id.
func (a *ResumeAccount) InsertUpworkCatalogItem(ctx context.Context, personID int, title, description string) (int, error) {
	if !a.writable() {
		return 0, ErrNoAccountScope
	}

	var id int
	err := a.db.pool.QueryRow(ctx, insertUpworkCatalogItemSQL, personID, title, description, a.aid).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert upwork catalog item: %w", err)
	}
	return id, nil
}

// DeleteUpworkCatalogItem removes an upwork_catalog_items row.
// WHERE clause includes person_id to prevent cross-person deletion.
func (a *ResumeAccount) DeleteUpworkCatalogItem(ctx context.Context, personID, id int) error {
	if !a.writable() {
		return ErrNoAccountScope
	}

	_, err := a.db.pool.Exec(ctx, deleteUpworkCatalogItemSQL, id, personID, a.aid)
	return err
}

// GetUpworkCatalogItemByID fetches a single upwork_catalog_items row by primary key.
func (a *ResumeAccount) GetUpworkCatalogItemByID(ctx context.Context, itemID int) (UpworkCatalogItem, error) {
	var c UpworkCatalogItem
	err := a.conn(ctx).QueryRow(ctx,
		`SELECT id, title, COALESCE(description,''), position FROM upwork_catalog_items WHERE id = $1 AND `+personOwnedBy2, itemID, a.aid).
		Scan(&c.ID, &c.Title, &c.Description, &c.Position)
	return c, err
}

// UpdateUpworkCatalogItem updates the title and description of a catalog item.
func (a *ResumeAccount) UpdateUpworkCatalogItem(ctx context.Context, itemID int, title, description string) error {
	if !a.writable() {
		return ErrNoAccountScope
	}

	_, err := a.conn(ctx).Exec(ctx,
		`UPDATE upwork_catalog_items SET title = $2, description = $3 WHERE id = $1 AND `+personOwnedBy4,
		itemID, title, description, a.aid)
	return err
}

// ReorderUpworkCatalogItems normalizes positions to contiguous 1..N for the given person.
// orderedIDs lists catalog item IDs in desired display order. Any item not in orderedIDs
// is appended after the supplied subset (stable by old position, id), ensuring the
// full set has contiguous positions with no gaps or duplicates.
//
//nolint:dupl // intentional parallel: same algorithm, different table/column/error-string
func (a *ResumeAccount) ReorderUpworkCatalogItems(ctx context.Context, personID int, orderedIDs []int) error {
	if !a.writable() {
		return ErrNoAccountScope
	}

	tx, err := a.db.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("reorder catalog items begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Fetch the full current set ordered by old position, id.
	rows, err := tx.Query(ctx, `SELECT id FROM upwork_catalog_items WHERE person_id = $1 AND EXISTS (SELECT 1 FROM resume_persons WHERE id = $1 AND account_id = $2) ORDER BY position, id`, personID, a.aid)
	if err != nil {
		return fmt.Errorf("reorder catalog items fetch current: %w", err)
	}
	var allIDs []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("reorder catalog items scan: %w", err)
		}
		allIDs = append(allIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("reorder catalog items rows: %w", err)
	}

	// Build final order: supplied IDs first, then remaining IDs not in supplied list.
	supplied := make(map[int]struct{}, len(orderedIDs))
	for _, id := range orderedIDs {
		supplied[id] = struct{}{}
	}
	finalOrder := make([]int, 0, len(allIDs))
	finalOrder = append(finalOrder, orderedIDs...)
	for _, id := range allIDs {
		if _, ok := supplied[id]; !ok {
			finalOrder = append(finalOrder, id)
		}
	}

	for i, id := range finalOrder {
		if _, execErr := tx.Exec(ctx,
			`UPDATE upwork_catalog_items SET position = $1 WHERE id = $2 AND person_id = $3 AND `+personOwnedBy4,
			i+1, id, personID, a.aid,
		); execErr != nil {
			return fmt.Errorf("reorder catalog item id=%d: %w", id, execErr)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("reorder catalog items commit: %w", err)
	}
	return nil
}

// ReorderUpworkSkills normalizes positions to contiguous 1..N for the given person.
// orderedIDs lists skill IDs in desired display order. Any skill not in orderedIDs
// is appended after the supplied subset (stable by old position, id), ensuring the
// full set has contiguous positions with no gaps or duplicates.
//
//nolint:dupl // intentional parallel: same algorithm, different table/column/error-string
func (a *ResumeAccount) ReorderUpworkSkills(ctx context.Context, personID int, orderedIDs []int) error {
	if !a.writable() {
		return ErrNoAccountScope
	}

	tx, err := a.db.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("reorder skills begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Fetch the full current set ordered by old position, id.
	rows, err := tx.Query(ctx, `SELECT id FROM upwork_skills WHERE person_id = $1 AND EXISTS (SELECT 1 FROM resume_persons WHERE id = $1 AND account_id = $2) ORDER BY position, id`, personID, a.aid)
	if err != nil {
		return fmt.Errorf("reorder skills fetch current: %w", err)
	}
	var allIDs []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("reorder skills scan: %w", err)
		}
		allIDs = append(allIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("reorder skills rows: %w", err)
	}

	// Build final order: supplied IDs first, then remaining IDs not in supplied list.
	supplied := make(map[int]struct{}, len(orderedIDs))
	for _, id := range orderedIDs {
		supplied[id] = struct{}{}
	}
	finalOrder := make([]int, 0, len(allIDs))
	finalOrder = append(finalOrder, orderedIDs...)
	for _, id := range allIDs {
		if _, ok := supplied[id]; !ok {
			finalOrder = append(finalOrder, id)
		}
	}

	for i, id := range finalOrder {
		if _, execErr := tx.Exec(ctx,
			`UPDATE upwork_skills SET position = $1 WHERE id = $2 AND person_id = $3 AND `+personOwnedBy4,
			i+1, id, personID, a.aid,
		); execErr != nil {
			return fmt.Errorf("reorder skill id=%d: %w", id, execErr)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("reorder skills commit: %w", err)
	}
	return nil
}
