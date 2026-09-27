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

// personOwnedBy2 / personOwnedBy3 are the account-ownership predicates for
// child-table statements (plan ADR-6): the row's parent person must be owned by
// the bound account, so an entity id from another account matches nothing —
// foreign rows read as absent and writes are no-ops.
const (
	personOwnedBy2 = `person_id IN (SELECT id FROM resume_persons WHERE account_id = $2)`
	personOwnedBy3 = `person_id IN (SELECT id FROM resume_persons WHERE account_id = $3)`
	personOwnedBy4 = `person_id IN (SELECT id FROM resume_persons WHERE account_id = $4)`
	personOwnedBy5 = `person_id IN (SELECT id FROM resume_persons WHERE account_id = $5)`
	personOwnedBy6 = `person_id IN (SELECT id FROM resume_persons WHERE account_id = $6)`
	personOwnedBy7 = `person_id IN (SELECT id FROM resume_persons WHERE account_id = $7)`
	personOwnedBy8 = `person_id IN (SELECT id FROM resume_persons WHERE account_id = $8)`
	personOwnedBy9 = `person_id IN (SELECT id FROM resume_persons WHERE account_id = $9)`
)

// Package-level SQL constants — single source of truth for every query in this
// file. Tests reference these directly so editing a query here will break
// the test (red-on-revert guaranteed).
const (
	deleteExperienceSQL   = `DELETE FROM resume_experiences WHERE id = $1 AND ` + personOwnedBy2
	deleteSkillSQL        = `DELETE FROM resume_skills WHERE id = $1 AND ` + personOwnedBy2
	deleteAchievementSQL  = `DELETE FROM resume_achievements WHERE id = $1 AND ` + personOwnedBy2
	deleteDomainSQL       = `DELETE FROM public.resume_domains WHERE id = $1 AND ` + personOwnedBy2
	deleteMethodologySQL  = `DELETE FROM public.resume_methodologies WHERE id = $1 AND ` + personOwnedBy2
	updateSkillLevelSQL   = `UPDATE resume_skills SET level = $2 WHERE id = $1 AND ` + personOwnedBy3
	updateResumePersonSQL = `UPDATE resume_persons
		 SET name = $2, email = $3, phone = $4, location = $5, links = $6, summary = $7,
		     updated_at = now()
		 WHERE id = $1 AND account_id = $8`
)

// DeleteExperience removes an experience row by primary key when the bound
// account owns its parent person; a foreign id is a silent no-op.
func (a *ResumeAccount) DeleteExperience(ctx context.Context, expID int) error {
	if !a.writable() {
		return ErrNoAccountScope
	}
	_, err := a.db.pool.Exec(ctx, deleteExperienceSQL, expID, a.aid)
	return err
}

// DeleteSkill removes a skill row by primary key when the bound account owns
// its parent person; a foreign id is a silent no-op.
func (a *ResumeAccount) DeleteSkill(ctx context.Context, skillID int) error {
	if !a.writable() {
		return ErrNoAccountScope
	}
	_, err := a.db.pool.Exec(ctx, deleteSkillSQL, skillID, a.aid)
	return err
}

// DeleteAchievement removes an achievement row by primary key when the bound
// account owns its parent person; a foreign id is a silent no-op.
func (a *ResumeAccount) DeleteAchievement(ctx context.Context, achvID int) error {
	if !a.writable() {
		return ErrNoAccountScope
	}
	_, err := a.db.pool.Exec(ctx, deleteAchievementSQL, achvID, a.aid)
	return err
}

// DeleteDomain removes a domain row by primary key when the bound account owns
// its parent person; a foreign id is a silent no-op.
func (a *ResumeAccount) DeleteDomain(ctx context.Context, domainID int) error {
	if !a.writable() {
		return ErrNoAccountScope
	}
	_, err := a.db.pool.Exec(ctx, deleteDomainSQL, domainID, a.aid)
	return err
}

// DeleteMethodology removes a methodology row by primary key when the bound
// account owns its parent person; a foreign id is a silent no-op.
func (a *ResumeAccount) DeleteMethodology(ctx context.Context, methID int) error {
	if !a.writable() {
		return ErrNoAccountScope
	}
	_, err := a.db.pool.Exec(ctx, deleteMethodologySQL, methID, a.aid)
	return err
}

// UpdateSkillLevel sets the level field for the given skill when the bound
// account owns its parent person; a foreign id is a silent no-op.
// The caller must validate level against IsValidSkillLevel before calling.
func (a *ResumeAccount) UpdateSkillLevel(ctx context.Context, skillID int, level string) error {
	if !a.writable() {
		return ErrNoAccountScope
	}
	_, err := a.db.pool.Exec(ctx, updateSkillLevelSQL, skillID, level, a.aid)
	return err
}

// UpdateResumePerson updates the editable header fields of a resume_persons
// row owned by the bound account; a foreign id is a silent no-op.
// Links is serialised as JSON; other fields are plain TEXT columns.
func (a *ResumeAccount) UpdateResumePerson(ctx context.Context, personID int, p PersonRecord) error {
	if !a.writable() {
		return ErrNoAccountScope
	}
	linksJSON, err := json.Marshal(p.Links)
	if err != nil {
		return fmt.Errorf("marshal links: %w", err)
	}
	_, err = a.db.pool.Exec(ctx, updateResumePersonSQL,
		personID, p.Name, p.Email, p.Phone, p.Location, linksJSON, p.Summary, a.aid)
	if err != nil {
		return err
	}
	// resume_persons.location changed in-process (the admin UI POST
	// /admin/resume/edit path) — drop this account's craigslist
	// profile-location cache so the connector re-reads instead of searching
	// the pre-edit city until a restart.
	invalidateProfileLocationCache(a.aid)
	return nil
}

// Package-level SQL constants for projects, educations, and certifications.
// Tests reference these directly so editing a query here will break
// the test (red-on-revert guaranteed).
const (
	deleteProjectSQL       = `DELETE FROM resume_projects WHERE id = $1 AND ` + personOwnedBy2
	deleteEducationSQL     = `DELETE FROM resume_educations WHERE id = $1 AND ` + personOwnedBy2
	deleteCertificationSQL = `DELETE FROM resume_certifications WHERE id = $1 AND ` + personOwnedBy2
)

// DeleteProject removes a project row by primary key when the bound account
// owns its parent person; a foreign id is a silent no-op.
func (a *ResumeAccount) DeleteProject(ctx context.Context, projectID int) error {
	if !a.writable() {
		return ErrNoAccountScope
	}
	_, err := a.db.pool.Exec(ctx, deleteProjectSQL, projectID, a.aid)
	return err
}

// DeleteEducation removes an education row by primary key when the bound
// account owns its parent person; a foreign id is a silent no-op.
func (a *ResumeAccount) DeleteEducation(ctx context.Context, educationID int) error {
	if !a.writable() {
		return ErrNoAccountScope
	}
	_, err := a.db.pool.Exec(ctx, deleteEducationSQL, educationID, a.aid)
	return err
}

// DeleteCertification removes a certification row by primary key when the
// bound account owns its parent person; a foreign id is a silent no-op.
func (a *ResumeAccount) DeleteCertification(ctx context.Context, certificationID int) error {
	if !a.writable() {
		return ErrNoAccountScope
	}
	_, err := a.db.pool.Exec(ctx, deleteCertificationSQL, certificationID, a.aid)
	return err
}

//nolint:gosec // updatePersonUpworkFieldsSQL is a SQL statement, not a credential
const updatePersonUpworkFieldsSQL = `
    UPDATE resume_persons SET headline = $2, hourly_rate = $3 WHERE id = $1 AND account_id = $4
`

// UpdatePersonUpworkFields updates the Upwork-specific fields (headline and
// hourly_rate) for a person owned by the bound account; a foreign id is a
// silent no-op.
func (a *ResumeAccount) UpdatePersonUpworkFields(ctx context.Context, personID int, headline string, hourlyRateCents int64) error {
	if !a.writable() {
		return ErrNoAccountScope
	}
	_, err := a.db.pool.Exec(ctx, updatePersonUpworkFieldsSQL, personID, headline, hourlyRateCents, a.aid)
	return err
}
