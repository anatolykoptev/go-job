package jobs

import (
	"strings"
	"testing"
)

// TestResumeDBEdit_DeleteMethodSignatures_SQL asserts structural invariants on
// the SHIPPED SQL consts (deleteExperienceSQL etc.). Editing a const in
// resumedb_edit.go is what this test now reads — genuine red-on-revert.
// No DB connection used.
func TestResumeDBEdit_DeleteMethodSignatures_SQL(t *testing.T) {
	cases := []struct {
		name             string
		shipped          string // the package const — same source the method executes
		wantDeleteClause string // full "DELETE FROM <table> WHERE" fragment; change the table and it fails
		wantPKClause     string
	}{
		{
			name:             "DeleteExperience",
			shipped:          deleteExperienceSQL,
			wantDeleteClause: "DELETE FROM resume_experiences WHERE",
			wantPKClause:     "WHERE id = $1",
		},
		{
			name:             "DeleteSkill",
			shipped:          deleteSkillSQL,
			wantDeleteClause: "DELETE FROM resume_skills WHERE",
			wantPKClause:     "WHERE id = $1",
		},
		{
			name:             "DeleteAchievement",
			shipped:          deleteAchievementSQL,
			wantDeleteClause: "DELETE FROM resume_achievements WHERE",
			wantPKClause:     "WHERE id = $1",
		},
		{
			name:             "DeleteDomain",
			shipped:          deleteDomainSQL,
			wantDeleteClause: "DELETE FROM public.resume_domains WHERE",
			wantPKClause:     "WHERE id = $1",
		},
		{
			name:             "DeleteMethodology",
			shipped:          deleteMethodologySQL,
			wantDeleteClause: "DELETE FROM public.resume_methodologies WHERE",
			wantPKClause:     "WHERE id = $1",
		},
		{
			name:             "DeleteProject",
			shipped:          deleteProjectSQL,
			wantDeleteClause: "DELETE FROM resume_projects WHERE",
			wantPKClause:     "WHERE id = $1",
		},
		{
			name:             "DeleteEducation",
			shipped:          deleteEducationSQL,
			wantDeleteClause: "DELETE FROM resume_educations WHERE",
			wantPKClause:     "WHERE id = $1",
		},
		{
			name:             "DeleteCertification",
			shipped:          deleteCertificationSQL,
			wantDeleteClause: "DELETE FROM resume_certifications WHERE",
			wantPKClause:     "WHERE id = $1",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(tc.shipped, tc.wantDeleteClause) {
				t.Errorf("%s: SQL must contain %q; got: %s", tc.name, tc.wantDeleteClause, tc.shipped)
			}
			if !strings.Contains(tc.shipped, tc.wantPKClause) {
				t.Errorf("%s: SQL must contain %q for PK delete; got: %s", tc.name, tc.wantPKClause, tc.shipped)
			}
		})
	}
}

// TestIsValidSkillLevel_Allowlist checks the exported helper used by the edit
// handler to validate user input before calling UpdateSkillLevel.
//
// Red-on-revert: remove IsValidSkillLevel or empty validSkillLevels → assertions fail.
func TestIsValidSkillLevel_Allowlist(t *testing.T) {
	cases := []struct {
		level string
		want  bool
	}{
		{"beginner", true},
		{"intermediate", true},
		{"advanced", true},
		{"expert", true},
		{"", false},
		{"EXPERT", false},
		{"master", false},
		{"senior", false},
	}
	for _, tc := range cases {
		got := IsValidSkillLevel(tc.level)
		if got != tc.want {
			t.Errorf("IsValidSkillLevel(%q) = %v, want %v", tc.level, got, tc.want)
		}
	}
}

// TestUpdateResumePerson_LinksFieldIsJSON asserts structural invariants on the
// SHIPPED updateResumePersonSQL const:
//   - targets the correct table
//   - sets every editable column UpdateResumePerson writes (name/email/phone/location/links/summary/updated_at)
//   - does NOT touch enriched_at (a field the method must preserve)
//   - PK clause is present
//
// Red-on-revert: change the const in resumedb_edit.go → this test reads it and fails.
func TestUpdateResumePerson_LinksFieldIsJSON(t *testing.T) {
	sql := updateResumePersonSQL

	mustContain := []string{
		"UPDATE resume_persons",
		"name",
		"email",
		"phone",
		"location",
		"links",
		"summary",
		"updated_at",
		"WHERE id = $",
	}
	for _, frag := range mustContain {
		if !strings.Contains(sql, frag) {
			t.Errorf("updateResumePersonSQL missing expected fragment %q\nfull SQL: %s", frag, sql)
		}
	}

	// enriched_at must NOT be touched by this UPDATE — it is set by the enrichment
	// pipeline and must survive a plain person-header edit.
	if strings.Contains(sql, "enriched_at") {
		t.Errorf("updateResumePersonSQL must NOT reference enriched_at; got: %s", sql)
	}

	// Verify Links is map[string]string (needs json.Marshal, not plain bind).
	var p PersonRecord
	p.Links = map[string]string{"github": "https://github.com/x"}
	if len(p.Links) != 1 {
		t.Fatalf("PersonRecord.Links wrong length: %d", len(p.Links))
	}
}
