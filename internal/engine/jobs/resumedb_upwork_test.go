package jobs

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/anatolykoptev/go_job/internal/dbtest"
)

// TestUpdatePersonUpworkFields_RoundTrip verifies that UpdatePersonUpworkFields
// persists headline and hourly_rate, and GetPerson reads them back.
// Requires DATABASE_URL to be set; skips otherwise; fatals if it points at a
// non-_test database.
//
// Red-on-revert: remove UpdatePersonUpworkFields or drop the DB columns →
// either the call fails or the values read back as zero/empty.
func TestUpdatePersonUpworkFields_RoundTrip(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	dbtest.RequireTestDB(t, dsn)

	ctx := context.Background()
	db, err := ConnectResumeDB(ctx, dsn)
	if err != nil {
		t.Fatalf("ConnectResumeDB: %v", err)
	}
	t.Cleanup(db.Close)

	// Insert a test person.
	personID, err := db.InsertPerson(ctx, PersonRecord{
		Name:  "Upwork Test Person",
		Email: "upwork-test@example.com",
	})
	if err != nil {
		t.Fatalf("InsertPerson: %v", err)
	}
	t.Cleanup(func() { _ = db.ClearPerson(ctx, personID) })

	// Call the function under test.
	const wantHeadline = "Staff Software Engineer | Go + Rust"
	const wantRate int64 = 17500 // $175.00/hr

	if err := db.UpdatePersonUpworkFields(ctx, personID, wantHeadline, wantRate); err != nil {
		t.Fatalf("UpdatePersonUpworkFields: %v", err)
	}

	// Read back via GetPerson — this is the real code path the handler uses.
	person, err := db.GetPerson(ctx, personID)
	if err != nil {
		t.Fatalf("GetPerson: %v", err)
	}
	if person.Headline != wantHeadline {
		t.Errorf("Headline: got %q, want %q", person.Headline, wantHeadline)
	}
	if person.HourlyRateCents != wantRate {
		t.Errorf("HourlyRateCents: got %d, want %d", person.HourlyRateCents, wantRate)
	}
}

// TestUpdatePersonUpworkFieldsSQL_Structure verifies the shipped SQL const
// targets the correct table and sets both Upwork columns.
// Red-on-revert: edit updatePersonUpworkFieldsSQL → this test reads it and fails.
func TestUpdatePersonUpworkFieldsSQL_Structure(t *testing.T) {
	sql := updatePersonUpworkFieldsSQL
	for _, frag := range []string{
		"UPDATE resume_persons",
		"headline",
		"hourly_rate",
		"WHERE id = $1",
	} {
		if !strings.Contains(sql, frag) {
			t.Errorf("updatePersonUpworkFieldsSQL missing %q\nSQL: %s", frag, sql)
		}
	}
}
