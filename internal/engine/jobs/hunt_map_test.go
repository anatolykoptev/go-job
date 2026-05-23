package jobs

import (
	"testing"
	"time"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// helpers

func bountyListingForTest(issueNum string) hunt.Bounty {
	return BountyListingToHunt(engine.BountyListing{
		Title:    "Fix bug",
		URL:      "https://github.com/org/repo/issues/1",
		Source:   "algora",
		IssueNum: issueNum,
	})
}

func remoteJobListingForTest() engine.RemoteJobListing {
	return engine.RemoteJobListing{
		Title:   "Go Engineer",
		Company: "ACME Inc",
		URL:     "https://remoteok.com/jobs/123",
		Source:  "remoteok",
		Posted:  "2026-05-22T14:23:18Z",
	}
}

// --- parseDollarAmount ---

// TestParseDollarAmount_Plain verifies plain integer dollar string.
func TestParseDollarAmount_Plain(t *testing.T) {
	assert.Equal(t, 1000, parseDollarAmount("$1000"))
}

// TestParseDollarAmount_Comma verifies comma-separated thousands.
func TestParseDollarAmount_Comma(t *testing.T) {
	assert.Equal(t, 5000, parseDollarAmount("$5,000"))
}

// TestParseDollarAmount_K verifies 'k' suffix means ×1000.
func TestParseDollarAmount_K(t *testing.T) {
	assert.Equal(t, 50000, parseDollarAmount("$50k"))
}

// TestParseDollarAmount_M verifies 'M' suffix means ×1_000_000.
func TestParseDollarAmount_M(t *testing.T) {
	assert.Equal(t, 1_500_000, parseDollarAmount("$1.5M"))
}

// TestParseDollarAmount_Decimal verifies fractional dollars are truncated.
func TestParseDollarAmount_Decimal(t *testing.T) {
	assert.Equal(t, 5, parseDollarAmount("$5.50"))
}

// TestParseDollarAmount_Negative verifies negative values return 0.
func TestParseDollarAmount_Negative(t *testing.T) {
	assert.Equal(t, 0, parseDollarAmount("-100"))
}

// TestParseDollarAmount_Empty verifies empty string returns 0.
func TestParseDollarAmount_Empty(t *testing.T) {
	assert.Equal(t, 0, parseDollarAmount(""))
}

// TestParseDollarAmount_Garbage verifies unparseable strings return 0.
func TestParseDollarAmount_Garbage(t *testing.T) {
	assert.Equal(t, 0, parseDollarAmount("abc"))
}

// TestParseDollarCents_Fractional verifies $5.50 → 500 cents (5 dollars × 100).
// parseDollarAmount truncates to int first, so $5.50 = 5 dollars = 500 cents.
func TestParseDollarCents_Fractional(t *testing.T) {
	assert.Equal(t, int64(500), parseDollarCents("$5.50"))
}

// --- parseTimestamp ---

// TestParseTimestamp_RFC3339 verifies RFC3339 timestamp parsing.
func TestParseTimestamp_RFC3339(t *testing.T) {
	result := parseTimestamp("2026-05-22T14:23:18Z")
	require.NotNil(t, result)
	assert.Equal(t, 2026, result.Year())
	assert.Equal(t, time.May, result.Month())
	assert.Equal(t, 22, result.Day())
}

// TestParseTimestamp_Date verifies short date "2006-01-02" format.
func TestParseTimestamp_Date(t *testing.T) {
	result := parseTimestamp("2026-05-22")
	require.NotNil(t, result)
	assert.Equal(t, 2026, result.Year())
}

// TestParseTimestamp_Empty verifies empty string returns nil.
func TestParseTimestamp_Empty(t *testing.T) {
	assert.Nil(t, parseTimestamp(""))
}

// TestParseTimestamp_Garbage verifies unparseable strings return nil.
func TestParseTimestamp_Garbage(t *testing.T) {
	assert.Nil(t, parseTimestamp("yesterday"))
}

// --- IssueNum trim "#" prefix ---

// TestBountyListingToHunt_IssueNumHash verifies that "#42" is parsed correctly.
// BountyListing sources emit IssueNum as "#42"; strconv.Atoi fails → issue_number was always 0.
func TestBountyListingToHunt_IssueNumHash(t *testing.T) {
	b := bountyListingForTest("#42")
	assert.Equal(t, 42, b.IssueNumber, "IssueNum '#42' must strip the '#' prefix before Atoi")
}

// TestBountyListingToHunt_IssueNumPlain verifies that "42" (no prefix) still works.
func TestBountyListingToHunt_IssueNumPlain(t *testing.T) {
	b := bountyListingForTest("42")
	assert.Equal(t, 42, b.IssueNumber)
}

// TestBountyListingToHunt_IssueNumEmpty verifies that "" yields 0.
func TestBountyListingToHunt_IssueNumEmpty(t *testing.T) {
	b := bountyListingForTest("")
	assert.Equal(t, 0, b.IssueNumber)
}

// --- RemoteJobListingToHunt (spec FAIL) ---

// TestRemoteJobListingToHunt_Basic verifies the spec-required mapper exists and maps fields.
func TestRemoteJobListingToHunt_Basic(t *testing.T) {
	r := remoteJobListingForTest()
	j := RemoteJobListingToHunt(r)
	assert.Equal(t, "Go Engineer", j.Title)
	assert.Equal(t, "ACME Inc", j.Company)
	assert.NotEmpty(t, j.DedupHash)
	assert.Equal(t, "remoteok", j.Source)
	assert.NotNil(t, j.PostedAt, "Posted field must be parsed into PostedAt")
}

// TestRemoteJobListingToHunt_Raw verifies Raw JSONB is populated.
func TestRemoteJobListingToHunt_Raw(t *testing.T) {
	r := remoteJobListingForTest()
	j := RemoteJobListingToHunt(r)
	assert.NotEmpty(t, j.Raw, "Raw must be populated from source struct")
}

// --- Raw JSONB in other mappers ---

// TestBountyListingToHunt_RawPopulated verifies Raw is populated.
func TestBountyListingToHunt_RawPopulated(t *testing.T) {
	b := BountyListingToHunt(engine.BountyListing{
		Title:  "Test",
		URL:    "https://example.com/1",
		Source: "algora",
	})
	assert.NotEmpty(t, b.Raw, "Raw must be populated from source struct")
}

// TestJobListingToHunt_RawPopulated verifies Raw is populated.
func TestJobListingToHunt_RawPopulated(t *testing.T) {
	j := JobListingToHunt(engine.JobListing{
		Title:   "Engineer",
		URL:     "https://company.com/jobs/1",
		Company: "ACME",
		Source:  "linkedin",
	})
	assert.NotEmpty(t, j.Raw, "Raw must be populated from source struct")
}

// TestFreelanceProjectToHunt_RawPopulated verifies Raw is populated.
func TestFreelanceProjectToHunt_RawPopulated(t *testing.T) {
	f := FreelanceProjectToHunt(engine.FreelanceProject{
		Title:    "Go project",
		URL:      "https://upwork.com/jobs/1",
		Platform: "upwork",
	})
	assert.NotEmpty(t, f.Raw, "Raw must be populated from source struct")
}

// TestSecurityProgramToHunt_RawPopulated verifies Raw is populated.
func TestSecurityProgramToHunt_RawPopulated(t *testing.T) {
	s := SecurityProgramToHunt(engine.SecurityProgram{
		Name:     "Target",
		URL:      "https://hackerone.com/programs/target",
		Platform: "hackerone",
	})
	assert.NotEmpty(t, s.Raw, "Raw must be populated from source struct")
}

// TestBountyListingToHunt_PostedAt verifies Posted string is parsed into PostedAt.
func TestBountyListingToHunt_PostedAt(t *testing.T) {
	b := BountyListingToHunt(engine.BountyListing{
		Title:  "Test",
		URL:    "https://example.com/2",
		Source: "algora",
		Posted: "2026-05-22",
	})
	assert.NotNil(t, b.PostedAt, "Posted date must be parsed into PostedAt")
}

// TestJobListingToHunt_PostedAt verifies Posted string is parsed into PostedAt.
func TestJobListingToHunt_PostedAt(t *testing.T) {
	j := JobListingToHunt(engine.JobListing{
		Title:   "Engineer",
		URL:     "https://company.com/jobs/2",
		Company: "ACME",
		Source:  "linkedin",
		Posted:  "2026-05-22T14:23:18Z",
	})
	assert.NotNil(t, j.PostedAt, "Posted timestamp must be parsed into PostedAt")
}

// --- Phase 3: status mapping ---

// TestBountyListingToHunt_PreservesStatus verifies Algora status→hunt status mapping.
// "open" → StatusOpen, "claimed"/"completed" → StatusMerged, "closed"/"cancelled" → StatusClosed.
func TestBountyListingToHunt_PreservesStatus(t *testing.T) {
	cases := []struct {
		sourceStatus string
		wantHunt     string
	}{
		{"", hunt.StatusOpen},
		{"open", hunt.StatusOpen},
		{"claimed", hunt.StatusMerged},
		{"completed", hunt.StatusMerged},
		{"closed", hunt.StatusClosed},
		{"cancelled", hunt.StatusClosed},
	}
	for _, tc := range cases {
		b := BountyListingToHunt(engine.BountyListing{
			Title:  "Test",
			URL:    "https://github.com/org/repo/issues/77",
			Source: "algora",
			Status: tc.sourceStatus,
		})
		assert.Equal(t, tc.wantHunt, b.Status,
			"sourceStatus=%q should map to %q", tc.sourceStatus, tc.wantHunt)
	}
}

// TestSecurityProgramToHunt_ArchivedMapsToArchived verifies Sherlock archived repos
// are mapped to StatusArchived in the hunt.Security record.
func TestSecurityProgramToHunt_ArchivedMapsToArchived(t *testing.T) {
	sec := SecurityProgramToHunt(engine.SecurityProgram{
		Name:     "Archived Protocol",
		URL:      "https://github.com/sherlock-audit/2024-01-archived-judging",
		Platform: "sherlock",
		Archived: true,
	})
	assert.Equal(t, hunt.StatusArchived, sec.Status,
		"archived security program must map to StatusArchived")
}

// TestSecurityProgramToHunt_NonArchivedMapsToOpen verifies non-archived programs → open.
func TestSecurityProgramToHunt_NonArchivedMapsToOpen(t *testing.T) {
	sec := SecurityProgramToHunt(engine.SecurityProgram{
		Name:     "Active Protocol",
		URL:      "https://github.com/sherlock-audit/2024-01-active-judging",
		Platform: "sherlock",
		Archived: false,
	})
	assert.Equal(t, hunt.StatusOpen, sec.Status)
}
