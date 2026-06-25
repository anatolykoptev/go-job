package huntworker

import (
	"strings"
	"testing"

	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeHuntNotifier records NotifyNewJob calls for worker unit tests.
type fakeHuntNotifier struct {
	jobs []hunt.Job
}

func (f *fakeHuntNotifier) NotifyNewBounty(b hunt.Bounty)                    {}
func (f *fakeHuntNotifier) NotifyNewJob(j hunt.Job, _ *hunt.ScoreResult)     { f.jobs = append(f.jobs, j) }
func (f *fakeHuntNotifier) NotifyNewFreelance(fr hunt.Freelance)             {}
func (f *fakeHuntNotifier) NotifyNewSecurity(s hunt.Security)                {}

// TestWorker_SetNotifier_Wires verifies SetNotifier assigns the notifier field.
func TestWorker_SetNotifier_Wires(t *testing.T) {
	w := &Worker{}
	n := &fakeHuntNotifier{}
	w.SetNotifier(n)
	assert.Equal(t, n, w.notifier, "SetNotifier must wire the notifier field")
}

// TestWorker_MaybeNotifyJob_Created_Open: OutcomeCreated + StatusOpen → notify fires.
func TestWorker_MaybeNotifyJob_Created_Open(t *testing.T) {
	f := &fakeHuntNotifier{}
	w := &Worker{notifier: f}
	j := hunt.Job{URL: "https://x.com/j", Status: hunt.StatusOpen}
	w.maybeNotifyJob(j, hunt.OutcomeCreated, nil)
	assert.Len(t, f.jobs, 1, "OutcomeCreated + open status must notify")
}

// TestWorker_MaybeNotifyJob_Created_EmptyStatus: empty Status treated as open (SearxngResultToHuntJob leaves Status="").
func TestWorker_MaybeNotifyJob_Created_EmptyStatus(t *testing.T) {
	f := &fakeHuntNotifier{}
	w := &Worker{notifier: f}
	j := hunt.Job{URL: "https://x.com/j", Status: ""}
	w.maybeNotifyJob(j, hunt.OutcomeCreated, nil)
	assert.Len(t, f.jobs, 1, "empty Status must be treated as open — SearxngResultToHuntJob leaves Status empty")
}

// TestWorker_MaybeNotifyJob_Merged_NoNotify: OutcomeMerged must not notify.
func TestWorker_MaybeNotifyJob_Merged_NoNotify(t *testing.T) {
	f := &fakeHuntNotifier{}
	w := &Worker{notifier: f}
	j := hunt.Job{URL: "https://x.com/j", Status: hunt.StatusOpen}
	w.maybeNotifyJob(j, hunt.OutcomeMerged, nil)
	assert.Empty(t, f.jobs, "OutcomeMerged must not notify")
}

// TestWorker_MaybeNotifyJob_NilNotifier: no panic when notifier is nil.
func TestWorker_MaybeNotifyJob_NilNotifier(t *testing.T) {
	w := &Worker{notifier: nil}
	j := hunt.Job{URL: "https://x.com/j", Status: hunt.StatusOpen}
	w.maybeNotifyJob(j, hunt.OutcomeCreated, nil) // must not panic
}

// TestWorker_MaybeNotifyJob_Closed_NoNotify: closed job must not notify even on create.
func TestWorker_MaybeNotifyJob_Closed_NoNotify(t *testing.T) {
	f := &fakeHuntNotifier{}
	w := &Worker{notifier: f}
	j := hunt.Job{URL: "https://x.com/j", Status: hunt.StatusClosed}
	w.maybeNotifyJob(j, hunt.OutcomeCreated, nil)
	assert.Empty(t, f.jobs, "closed status must not notify")
}

func TestParseQueries_Basic(t *testing.T) {
	got := parseQueries("golang developer, backend engineer, ")
	assert.Equal(t, []string{"golang developer", "backend engineer"}, got)
}

func TestParseQueries_Empty_UsesDefault(t *testing.T) {
	got := parseQueries("")
	assert.NotEmpty(t, got)
	// Default must not contain any ATS slugs (fitness function: no boards.greenhouse.io/X literals).
	for _, q := range got {
		assert.NotContains(t, q, "boards.greenhouse.io/")
		assert.NotContains(t, q, "jobs.lever.co/")
		assert.NotContains(t, q, "jobs.ashbyhq.com/")
	}
}

func TestHuntIngestEnabled_DefaultFalse(t *testing.T) {
	// HUNT_INGEST_ENABLED is not set in the test environment.
	t.Setenv("HUNT_INGEST_ENABLED", "")
	assert.False(t, huntIngestEnabled())
}

func TestHuntIngestEnabled_TrueWhenSet(t *testing.T) {
	t.Setenv("HUNT_INGEST_ENABLED", "true")
	assert.True(t, huntIngestEnabled())
}

func TestNewWorker_NilStore_ReturnsNil(t *testing.T) {
	w := NewWorker(nil)
	assert.Nil(t, w)
}

// TestNoCompanyTargetingInDefaults is the fitness function (ADR-002 / P1 design):
// go-job is a PUBLIC repo — personal target companies must never be baked into
// the shipped default queries.  The check covers both URL-slug form AND bare
// company names, so re-introducing targeting under either form fails the test.
//
// The set below is NOT exhaustive — it is sampled representative examples of
// the class of company-specific strings that must be absent.  When adding a new
// test query, prefer generic role/skill language, not employer names.
func TestNoCompanyTargetingInDefaults(t *testing.T) {
	// Representative company names / ATS slugs that must never appear in defaults.
	// These are PUBLIC well-known entities — listing them here is NOT a personal
	// target list; it is an enumeration of the class of strings to block.
	forbiddenPatterns := []string{
		// URL-slug forms (any ATS).
		"boards.greenhouse.io/",
		"jobs.lever.co/",
		"jobs.ashbyhq.com/",
		// Symbolic guard vars that might sneak in.
		"seedOrgs", "knownOrgs",
		// Bare company names (representative sample of the class).
		// If these appear in a query string it means someone added company-specific targeting.
		"stripe", "openai", "anthropic", "google", "apple", "meta",
		"netflix", "airbnb", "uber", "lyft", "coinbase",
	}

	queries := parseQueries(defaultIngestQueries)
	require.NotEmpty(t, queries, "default queries must not be empty")

	for _, q := range queries {
		lower := strings.ToLower(q)
		for _, forbidden := range forbiddenPatterns {
			assert.NotContains(t, lower, strings.ToLower(forbidden),
				"default query %q must not contain company-specific targeting %q (PUBLIC repo — ADR-002)",
				q, forbidden)
		}
	}
}
