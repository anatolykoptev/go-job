package jobs

// habr_employment_test.go guards the tolerant habrEmployment decoder (P4(b)).
//
// Revert-red: removing habrEmployment and reverting Employment back to
// `struct{Title string}` makes TestHabrEmployment_StringFormat fail immediately
// (json.Unmarshal of a plain string into a struct is an error).

import (
	"encoding/json"
	"testing"
)

// TestHabrEmployment_StringFormat — the primary regression guard.
// Habr Career switched from {"employment":{"title":"Full-time"}} to
// {"employment":"Full-time"} (schema drift 2026). This path was the root
// cause of outcome=parse_fail on every Habr response.
func TestHabrEmployment_StringFormat(t *testing.T) {
	input := `"Full-time"`
	var e habrEmployment
	if err := json.Unmarshal([]byte(input), &e); err != nil {
		t.Fatalf("UnmarshalJSON(string) = %v; want nil", err)
	}
	if e.Title != "Full-time" {
		t.Errorf("Title = %q; want %q", e.Title, "Full-time")
	}
}

// TestHabrEmployment_ObjectFormat — backward-compatibility for the legacy shape.
func TestHabrEmployment_ObjectFormat(t *testing.T) {
	input := `{"title":"Part-time"}`
	var e habrEmployment
	if err := json.Unmarshal([]byte(input), &e); err != nil {
		t.Fatalf("UnmarshalJSON(object) = %v; want nil", err)
	}
	if e.Title != "Part-time" {
		t.Errorf("Title = %q; want %q", e.Title, "Part-time")
	}
}

// TestHabrEmployment_EmptyString — empty string does not error.
func TestHabrEmployment_EmptyString(t *testing.T) {
	var e habrEmployment
	if err := json.Unmarshal([]byte(`""`), &e); err != nil {
		t.Fatalf("UnmarshalJSON(\"\") = %v; want nil", err)
	}
	if e.Title != "" {
		t.Errorf("Title = %q; want empty", e.Title)
	}
}

// TestHabrVacancy_StringEmployment ensures a full vacancy with the new string
// employment format decodes without error — this is the exact doc shape that
// caused outcome=parse_fail before P4(b).
func TestHabrVacancy_StringEmployment(t *testing.T) {
	// Minimal vacancy with the current (string) employment format.
	doc := `{
		"id": 1234567,
		"title": "Senior Go Engineer",
		"href": "/vacancies/1234567",
		"company": {"title": "Яндекс", "href": "/companies/yandex"},
		"salary": {"from": 300000, "to": 500000, "currency": "RUB"},
		"skills": [{"title": "Go"}, {"title": "Kubernetes"}],
		"locations": [{"title": "Москва"}],
		"remoteWork": true,
		"publishedAt": "2026-06-20T10:00:00+03:00",
		"employment": "Full-time"
	}`
	var v habrVacancy
	if err := json.Unmarshal([]byte(doc), &v); err != nil {
		t.Fatalf("Unmarshal vacancy (string employment) = %v; want nil — "+
			"this is the schema-drift that caused outcome=parse_fail", err)
	}
	if v.Employment.Title != "Full-time" {
		t.Errorf("Employment.Title = %q; want %q", v.Employment.Title, "Full-time")
	}
	if v.Title != "Senior Go Engineer" {
		t.Errorf("Title = %q; unexpected", v.Title)
	}
}

// TestHabrVacancy_ObjectEmployment ensures the legacy object format still works.
func TestHabrVacancy_ObjectEmployment(t *testing.T) {
	doc := `{
		"id": 999,
		"title": "Backend Developer",
		"href": "/vacancies/999",
		"company": {"title": "EPAM", "href": "/companies/epam"},
		"employment": {"title": "Contract"}
	}`
	var v habrVacancy
	if err := json.Unmarshal([]byte(doc), &v); err != nil {
		t.Fatalf("Unmarshal vacancy (object employment) = %v; want nil", err)
	}
	if v.Employment.Title != "Contract" {
		t.Errorf("Employment.Title = %q; want %q", v.Employment.Title, "Contract")
	}
}
