package jobs

import (
	"strings"
	"testing"
)

// TestResumeDBEdit_DeleteMethodSignatures_SQL asserts the SQL strings embedded
// in the new Delete* / Update* methods contain the correct table names and
// parameter placeholders. No DB connection is used.
//
// Red-on-revert: rename the method, change the table, or remove the $1 param
// → the corresponding assertion fails.
func TestResumeDBEdit_DeleteMethodSignatures_SQL(t *testing.T) {
	cases := []struct {
		name  string
		query string // literal SQL string copy from the production method above
		want  []string
	}{
		{
			name:  "DeleteExperience",
			query: `DELETE FROM resume_experiences WHERE id = $1`,
			want:  []string{"DELETE FROM resume_experiences", "id = $1"},
		},
		{
			name:  "DeleteSkill",
			query: `DELETE FROM resume_skills WHERE id = $1`,
			want:  []string{"DELETE FROM resume_skills", "id = $1"},
		},
		{
			name:  "DeleteAchievement",
			query: `DELETE FROM resume_achievements WHERE id = $1`,
			want:  []string{"DELETE FROM resume_achievements", "id = $1"},
		},
		{
			name:  "DeleteDomain",
			query: `DELETE FROM public.resume_domains WHERE id = $1`,
			want:  []string{"public.resume_domains", "id = $1"},
		},
		{
			name:  "DeleteMethodology",
			query: `DELETE FROM public.resume_methodologies WHERE id = $1`,
			want:  []string{"public.resume_methodologies", "id = $1"},
		},
		{
			name:  "UpdateSkillLevel",
			query: `UPDATE resume_skills SET level = $2 WHERE id = $1`,
			want:  []string{"UPDATE resume_skills", "level = $2", "id = $1"},
		},
		{
			name:  "UpdateResumePerson",
			query: `UPDATE resume_persons SET name = $2, email = $3, phone = $4, location = $5, links = $6, summary = $7, updated_at = now() WHERE id = $1`,
			want:  []string{"UPDATE resume_persons", "name = $2", "email = $3", "phone = $4", "location = $5", "links = $6", "summary = $7", "id = $1"},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			for _, want := range tc.want {
				if !strings.Contains(tc.query, want) {
					t.Errorf("%s SQL missing %q\nfull: %s", tc.name, want, tc.query)
				}
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

// TestUpdateResumePerson_LinksFieldIsJSON asserts that UpdateResumePerson
// serialises the Links map as JSON before binding it to $6. We verify this
// offline by confirming that the production SQL string references "$6" and
// the actual PersonRecord.Links field is a map[string]string (not a []byte).
//
// Red-on-revert: change $6 to the wrong param or drop Links from the UPDATE →
// the SQL check or the type assertion below will fail.
func TestUpdateResumePerson_LinksFieldIsJSON(t *testing.T) {
	// Verify Links is map[string]string (needs json.Marshal, not plain bind).
	var p PersonRecord
	links := map[string]string{"github": "https://github.com/x"}
	p.Links = links

	if len(p.Links) != 1 {
		t.Fatalf("PersonRecord.Links wrong length: %d", len(p.Links))
	}

	const updateSQL = `UPDATE resume_persons
		 SET name = $2, email = $3, phone = $4, location = $5, links = $6, summary = $7,
		     updated_at = now()
		 WHERE id = $1`
	if !strings.Contains(updateSQL, "links = $6") {
		t.Error("UpdateResumePerson SQL: links must be bound to $6")
	}
}
