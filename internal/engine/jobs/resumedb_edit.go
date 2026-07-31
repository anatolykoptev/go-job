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

// Package-level SQL constants — single source of truth for every query in this
// file. Tests reference these directly so editing a query here will break
// the test (red-on-revert guaranteed).
const (
	deleteExperienceSQL   = `DELETE FROM resume_experiences WHERE id = $1`
	deleteSkillSQL        = `DELETE FROM resume_skills WHERE id = $1`
	deleteAchievementSQL  = `DELETE FROM resume_achievements WHERE id = $1`
	deleteDomainSQL       = `DELETE FROM public.resume_domains WHERE id = $1`
	deleteMethodologySQL  = `DELETE FROM public.resume_methodologies WHERE id = $1`
	updateSkillLevelSQL   = `UPDATE resume_skills SET level = $2 WHERE id = $1`
	updateResumePersonSQL = `UPDATE resume_persons
		 SET name = $2, email = $3, phone = $4, location = $5, links = $6, summary = $7,
		     updated_at = now()
		 WHERE id = $1`
)

// DeleteExperience removes an experience row by primary key.
// The PK-only delete is safe: experience IDs come from GetAllExperiences
// which is already person-scoped, so cross-person deletion cannot occur via
// the normal edit UI path.
// NOTE: absence of person_id scope in WHERE is safe ONLY under the single-user
// invariant; if this DB ever becomes multi-person these must be person-scoped.
func (db *ResumeDB) DeleteExperience(ctx context.Context, expID int) error {
	_, err := db.pool.Exec(ctx, deleteExperienceSQL, expID)
	return err
}

// DeleteSkill removes a skill row by primary key.
// NOTE: absence of person_id scope in WHERE is safe ONLY under the single-user
// invariant; if this DB ever becomes multi-person these must be person-scoped.
func (db *ResumeDB) DeleteSkill(ctx context.Context, skillID int) error {
	_, err := db.pool.Exec(ctx, deleteSkillSQL, skillID)
	return err
}

// DeleteAchievement removes an achievement row by primary key.
// NOTE: absence of person_id scope in WHERE is safe ONLY under the single-user
// invariant; if this DB ever becomes multi-person these must be person-scoped.
func (db *ResumeDB) DeleteAchievement(ctx context.Context, achvID int) error {
	_, err := db.pool.Exec(ctx, deleteAchievementSQL, achvID)
	return err
}

// DeleteDomain removes a domain row by primary key.
// NOTE: absence of person_id scope in WHERE is safe ONLY under the single-user
// invariant; if this DB ever becomes multi-person these must be person-scoped.
func (db *ResumeDB) DeleteDomain(ctx context.Context, domainID int) error {
	_, err := db.pool.Exec(ctx, deleteDomainSQL, domainID)
	return err
}

// DeleteMethodology removes a methodology row by primary key.
// NOTE: absence of person_id scope in WHERE is safe ONLY under the single-user
// invariant; if this DB ever becomes multi-person these must be person-scoped.
func (db *ResumeDB) DeleteMethodology(ctx context.Context, methID int) error {
	_, err := db.pool.Exec(ctx, deleteMethodologySQL, methID)
	return err
}

// UpdateSkillLevel sets the level field for the given skill.
// The caller must validate level against IsValidSkillLevel before calling.
// NOTE: absence of person_id scope in WHERE is safe ONLY under the single-user
// invariant; if this DB ever becomes multi-person these must be person-scoped.
func (db *ResumeDB) UpdateSkillLevel(ctx context.Context, skillID int, level string) error {
	_, err := db.pool.Exec(ctx, updateSkillLevelSQL, skillID, level)
	return err
}

// UpdateResumePerson updates the editable header fields of a resume_persons row.
// Links is serialised as JSON; other fields are plain TEXT columns.
func (db *ResumeDB) UpdateResumePerson(ctx context.Context, personID int, p PersonRecord) error {
	linksJSON, err := json.Marshal(p.Links)
	if err != nil {
		return fmt.Errorf("marshal links: %w", err)
	}
	_, err = db.pool.Exec(ctx, updateResumePersonSQL,
		personID, p.Name, p.Email, p.Phone, p.Location, linksJSON, p.Summary)
	if err != nil {
		return err
	}
	// resume_persons.location changed in-process (the admin UI POST
	// /admin/resume/edit path) — drop the craigslist profile-location cache so
	// the connector re-reads instead of searching the pre-edit city until a
	// restart.
	invalidateProfileLocationCache()
	return nil
}

// Package-level SQL constants for projects, educations, and certifications.
// Tests reference these directly so editing a query here will break the test
// (red-on-revert guaranteed).
const (
	deleteProjectSQL       = `DELETE FROM resume_projects WHERE id = $1`
	deleteEducationSQL     = `DELETE FROM resume_educations WHERE id = $1`
	deleteCertificationSQL = `DELETE FROM resume_certifications WHERE id = $1`
)

// DeleteProject removes a project row by primary key.
// NOTE: absence of person_id scope in WHERE is safe ONLY under the single-user
// invariant; if this DB ever becomes multi-person these must be person-scoped.
func (db *ResumeDB) DeleteProject(ctx context.Context, projectID int) error {
	_, err := db.pool.Exec(ctx, deleteProjectSQL, projectID)
	return err
}

// DeleteEducation removes an education row by primary key.
// NOTE: absence of person_id scope in WHERE is safe ONLY under the single-user
// invariant; if this DB ever becomes multi-person these must be person-scoped.
func (db *ResumeDB) DeleteEducation(ctx context.Context, educationID int) error {
	_, err := db.pool.Exec(ctx, deleteEducationSQL, educationID)
	return err
}

// DeleteCertification removes a certification row by primary key.
// NOTE: absence of person_id scope in WHERE is safe ONLY under the single-user
// invariant; if this DB ever becomes multi-person these must be person-scoped.
func (db *ResumeDB) DeleteCertification(ctx context.Context, certificationID int) error {
	_, err := db.pool.Exec(ctx, deleteCertificationSQL, certificationID)
	return err
}

//nolint:gosec // updatePersonUpworkFieldsSQL is a SQL statement, not a credential
const updatePersonUpworkFieldsSQL = `
    UPDATE resume_persons SET headline = $2, hourly_rate = $3 WHERE id = $1
`

// UpdatePersonUpworkFields updates the Upwork-specific fields (headline and hourly_rate) for a person.
// NOTE: absence of person_id scope in WHERE is safe ONLY under the single-user
// invariant; if this DB ever becomes multi-person these must be person-scoped.
func (db *ResumeDB) UpdatePersonUpworkFields(ctx context.Context, personID int, headline string, hourlyRateCents int64) error {
	_, err := db.pool.Exec(ctx, updatePersonUpworkFieldsSQL, personID, headline, hourlyRateCents)
	return err
}
