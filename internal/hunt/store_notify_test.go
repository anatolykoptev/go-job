package hunt_test

// Tests for BLOCKER: notifier must not fire on non-open status.
// Ref: PR #19 code-quality review — BLOCKER finding.

import (
	"context"
	"testing"

	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockNotifier records calls to each Notify method.
type mockNotifier struct {
	bounties  []hunt.Bounty
	jobs      []hunt.Job
	freelance []hunt.Freelance
	security  []hunt.Security
}

func (m *mockNotifier) NotifyNewBounty(b hunt.Bounty)       { m.bounties = append(m.bounties, b) }
func (m *mockNotifier) NotifyNewJob(j hunt.Job)             { m.jobs = append(m.jobs, j) }
func (m *mockNotifier) NotifyNewFreelance(f hunt.Freelance) { m.freelance = append(m.freelance, f) }
func (m *mockNotifier) NotifyNewSecurity(s hunt.Security)   { m.security = append(m.security, s) }

// TestUpsert_DoesNotNotifyClosedBounty verifies that notifier.NotifyNewBounty is NOT
// called when a new bounty is inserted with Status != open (e.g. claimed/completed).
// BLOCKER: first deploy + Algora full scrape would blast Telegram for every historical
// claimed bounty if this guard were absent.
func TestUpsert_DoesNotNotifyClosedBounty(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))
	truncateBounties(t, pool)

	notifier := &mockNotifier{}
	s.SetNotifier(notifier)

	// Insert a bounty that is already closed (e.g. Algora historical claimed bounty).
	closed := hunt.Bounty{
		DedupHash: hunt.DedupHash("https://github.com/org/repo/issues/notify-test-closed"),
		Title:     "Already claimed",
		URL:       "https://github.com/org/repo/issues/notify-test-closed",
		Source:    "algora",
		Status:    hunt.StatusClosed,
	}
	_, outcome, err := s.UpsertBounty(ctx, closed)
	require.NoError(t, err)
	assert.Equal(t, hunt.OutcomeCreated, outcome, "closed bounty must still be inserted")
	assert.Empty(t, notifier.bounties,
		"notifier must NOT fire for closed bounty — prevents Telegram blast on first deploy")
}

// TestUpsert_DoesNotNotifyMergedBounty verifies merged bounties are also suppressed.
func TestUpsert_DoesNotNotifyMergedBounty(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))
	truncateBounties(t, pool)

	notifier := &mockNotifier{}
	s.SetNotifier(notifier)

	merged := hunt.Bounty{
		DedupHash: hunt.DedupHash("https://github.com/org/repo/issues/notify-test-merged"),
		Title:     "Already merged",
		URL:       "https://github.com/org/repo/issues/notify-test-merged",
		Source:    "algora",
		Status:    hunt.StatusMerged,
	}
	_, _, err := s.UpsertBounty(ctx, merged)
	require.NoError(t, err)
	assert.Empty(t, notifier.bounties, "notifier must NOT fire for merged bounty")
}

// TestUpsert_NotifiesOpenBounty verifies that open bounties still trigger the notifier.
func TestUpsert_NotifiesOpenBounty(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))
	truncateBounties(t, pool)

	notifier := &mockNotifier{}
	s.SetNotifier(notifier)

	open := hunt.Bounty{
		DedupHash: hunt.DedupHash("https://github.com/org/repo/issues/notify-test-open"),
		Title:     "New open bounty",
		URL:       "https://github.com/org/repo/issues/notify-test-open",
		Source:    "algora",
		Status:    hunt.StatusOpen,
	}
	_, outcome, err := s.UpsertBounty(ctx, open)
	require.NoError(t, err)
	assert.Equal(t, hunt.OutcomeCreated, outcome)
	assert.Len(t, notifier.bounties, 1, "notifier MUST fire for open bounty")
}

// TestUpsert_DoesNotNotifyClosedJob verifies job notifier is also gated on open status.
func TestUpsert_DoesNotNotifyClosedJob(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))
	truncateJobs(t, pool)

	notifier := &mockNotifier{}
	s.SetNotifier(notifier)

	closed := hunt.Job{
		DedupHash: hunt.DedupHash("https://company.com/jobs/notify-closed"),
		Title:     "Already closed position",
		URL:       "https://company.com/jobs/notify-closed",
		Source:    "linkedin",
		Status:    hunt.StatusClosed,
	}
	_, _, err := s.UpsertJob(ctx, closed)
	require.NoError(t, err)
	assert.Empty(t, notifier.jobs, "notifier must NOT fire for closed job")
}

// TestUpsert_DoesNotNotifyClosedFreelance verifies freelance notifier is gated on open status.
func TestUpsert_DoesNotNotifyClosedFreelance(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))
	truncateFreelance(t, pool)

	notifier := &mockNotifier{}
	s.SetNotifier(notifier)

	closed := hunt.Freelance{
		DedupHash: hunt.DedupHash("https://upwork.com/fl/notify-closed"),
		Title:     "Already archived project",
		URL:       "https://upwork.com/fl/notify-closed",
		Platform:  "upwork",
		Source:    "upwork",
		Status:    hunt.StatusArchived,
	}
	_, _, err := s.UpsertFreelance(ctx, closed)
	require.NoError(t, err)
	assert.Empty(t, notifier.freelance, "notifier must NOT fire for archived freelance")
}

// TestUpsert_DoesNotNotifyClosedSecurity verifies security notifier is gated on open status.
func TestUpsert_DoesNotNotifyClosedSecurity(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))
	truncateSecurity(t, pool)

	notifier := &mockNotifier{}
	s.SetNotifier(notifier)

	archived := hunt.Security{
		DedupHash: hunt.DedupHash("https://hackerone.com/programs/notify-archived"),
		Name:      "Archived Program",
		URL:       "https://hackerone.com/programs/notify-archived",
		Platform:  "hackerone",
		Status:    hunt.StatusArchived,
	}
	_, _, err := s.UpsertSecurity(ctx, archived)
	require.NoError(t, err)
	assert.Empty(t, notifier.security, "notifier must NOT fire for archived security program")
}

// TestUpsert_NeverNotifiesJob verifies that after phase-3 relocation, UpsertJob does
// NOT call NotifyNewJob even for an open job. Notify is now the worker's responsibility.
// RED before the store.go change; GREEN after removing the notifier call from UpsertJob.
func TestUpsert_NeverNotifiesJob(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))
	truncateJobs(t, pool)

	notifier := &mockNotifier{}
	s.SetNotifier(notifier)

	open := hunt.Job{
		DedupHash: hunt.DedupHash("https://company.com/jobs/notify-open-p3"),
		Title:     "Open position",
		URL:       "https://company.com/jobs/notify-open-p3",
		Source:    "greenhouse",
		Status:    hunt.StatusOpen,
	}
	_, outcome, err := s.UpsertJob(ctx, open)
	require.NoError(t, err)
	assert.Equal(t, hunt.OutcomeCreated, outcome)
	// Phase 3: notify relocated to runCycle — UpsertJob must NOT call NotifyNewJob.
	assert.Empty(t, notifier.jobs,
		"UpsertJob must NOT notify (phase 3: notify relocated to runCycle)")
}

// TestStore_NotifyJobIfOpen_OpenJob verifies the new helper notifies for an open job.
func TestStore_NotifyJobIfOpen_OpenJob(t *testing.T) {
	notifier := &mockNotifier{}
	s := hunt.NewStore(nil) // no pool needed — NotifyJobIfOpen is pure notify
	s.SetNotifier(notifier)
	j := hunt.Job{URL: "https://x.com/j", Status: hunt.StatusOpen}
	s.NotifyJobIfOpen(j)
	assert.Len(t, notifier.jobs, 1, "NotifyJobIfOpen must fire for open job")
}

// TestStore_NotifyJobIfOpen_EmptyStatus treats empty status as open (worker path).
func TestStore_NotifyJobIfOpen_EmptyStatus(t *testing.T) {
	notifier := &mockNotifier{}
	s := hunt.NewStore(nil)
	s.SetNotifier(notifier)
	j := hunt.Job{URL: "https://x.com/j", Status: ""}
	s.NotifyJobIfOpen(j)
	assert.Len(t, notifier.jobs, 1, "NotifyJobIfOpen must fire for empty-status job (treated as open)")
}

// TestStore_NotifyJobIfOpen_ClosedJob_NoNotify verifies closed jobs are silenced.
func TestStore_NotifyJobIfOpen_ClosedJob_NoNotify(t *testing.T) {
	notifier := &mockNotifier{}
	s := hunt.NewStore(nil)
	s.SetNotifier(notifier)
	j := hunt.Job{URL: "https://x.com/j", Status: hunt.StatusClosed}
	s.NotifyJobIfOpen(j)
	assert.Empty(t, notifier.jobs, "NotifyJobIfOpen must NOT fire for closed job")
}

// TestStore_NotifyJobIfOpen_NilNotifier verifies no panic when notifier is not wired.
func TestStore_NotifyJobIfOpen_NilNotifier(t *testing.T) {
	s := hunt.NewStore(nil) // no notifier wired
	j := hunt.Job{URL: "https://x.com/j", Status: hunt.StatusOpen}
	s.NotifyJobIfOpen(j) // must not panic
}
