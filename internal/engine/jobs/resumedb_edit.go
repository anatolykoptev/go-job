package jobs

import (
	"context"
	"encoding/json"
	"fmt"
)

// validSkillLevels is the allowlist for skill levels matching the schema convention.
var validSkillLevels = map[string]bool{
	"beginner":     true,
	"intermediate": true,
	"advanced":     true,
	"expert":       true,
}

// IsValidSkillLevel reports whether the given level string is in the allowlist.
func IsValidSkillLevel(level string) bool {
	return validSkillLevels[level]
}

// DeleteExperience removes an experience row by primary key.
// The PK-only delete is safe: experience IDs come from GetAllExperiences
// which is already person-scoped, so cross-person deletion cannot occur via
// the normal edit UI path.
func (db *ResumeDB) DeleteExperience(ctx context.Context, expID int) error {
	_, err := db.pool.Exec(ctx,
		`DELETE FROM resume_experiences WHERE id = $1`, expID)
	return err
}

// DeleteSkill removes a skill row by primary key.
func (db *ResumeDB) DeleteSkill(ctx context.Context, skillID int) error {
	_, err := db.pool.Exec(ctx,
		`DELETE FROM resume_skills WHERE id = $1`, skillID)
	return err
}

// DeleteAchievement removes an achievement row by primary key.
func (db *ResumeDB) DeleteAchievement(ctx context.Context, achvID int) error {
	_, err := db.pool.Exec(ctx,
		`DELETE FROM resume_achievements WHERE id = $1`, achvID)
	return err
}

// DeleteDomain removes a domain row by primary key.
func (db *ResumeDB) DeleteDomain(ctx context.Context, domainID int) error {
	_, err := db.pool.Exec(ctx,
		`DELETE FROM public.resume_domains WHERE id = $1`, domainID)
	return err
}

// DeleteMethodology removes a methodology row by primary key.
func (db *ResumeDB) DeleteMethodology(ctx context.Context, methID int) error {
	_, err := db.pool.Exec(ctx,
		`DELETE FROM public.resume_methodologies WHERE id = $1`, methID)
	return err
}

// UpdateSkillLevel sets the level field for the given skill.
// The caller must validate level against IsValidSkillLevel before calling.
func (db *ResumeDB) UpdateSkillLevel(ctx context.Context, skillID int, level string) error {
	_, err := db.pool.Exec(ctx,
		`UPDATE resume_skills SET level = $2 WHERE id = $1`, skillID, level)
	return err
}

// UpdateResumePerson updates the editable header fields of a resume_persons row.
// Links is serialised as JSON; other fields are plain TEXT columns.
func (db *ResumeDB) UpdateResumePerson(ctx context.Context, personID int, p PersonRecord) error {
	linksJSON, err := json.Marshal(p.Links)
	if err != nil {
		return fmt.Errorf("marshal links: %w", err)
	}
	_, err = db.pool.Exec(ctx,
		`UPDATE resume_persons
		 SET name = $2, email = $3, phone = $4, location = $5, links = $6, summary = $7,
		     updated_at = now()
		 WHERE id = $1`,
		personID, p.Name, p.Email, p.Phone, p.Location, linksJSON, p.Summary)
	return err
}
