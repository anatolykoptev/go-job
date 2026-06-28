package jobs

import (
	"sync"
	"testing"

	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeNotifier records notify calls for policy tests.
// It satisfies hunt.Notifier.
type fakeNotifier struct {
	mu        sync.Mutex
	bounties  []hunt.Bounty
	security  []hunt.Security
	freelance []hunt.Freelance
	jobs      []hunt.Job
}

func (f *fakeNotifier) NotifyNewBounty(b hunt.Bounty)            { f.mu.Lock(); defer f.mu.Unlock(); f.bounties = append(f.bounties, b) }
func (f *fakeNotifier) NotifyNewSecurity(s hunt.Security)        { f.mu.Lock(); defer f.mu.Unlock(); f.security = append(f.security, s) }
func (f *fakeNotifier) NotifyNewFreelance(fr hunt.Freelance)     { f.mu.Lock(); defer f.mu.Unlock(); f.freelance = append(f.freelance, fr) }
func (f *fakeNotifier) NotifyNewJob(j hunt.Job, _ *hunt.ScoreResult) { f.mu.Lock(); defer f.mu.Unlock(); f.jobs = append(f.jobs, j) }

// --- isUrgent threshold tests ---

func TestIsUrgentBounty_AboveThreshold(t *testing.T) {
	t.Setenv("HUNT_NOTIFY_MIN_BOUNTY_USD", "250")
	b := hunt.Bounty{AmountCents: 25001} // $250.01
	assert.True(t, isUrgentBounty(b), "bounty at $250.01 must be urgent (threshold $250)")
}

func TestIsUrgentBounty_AtThreshold(t *testing.T) {
	t.Setenv("HUNT_NOTIFY_MIN_BOUNTY_USD", "250")
	b := hunt.Bounty{AmountCents: 25000} // exactly $250
	assert.True(t, isUrgentBounty(b), "bounty at exactly $250 must be urgent")
}

func TestIsUrgentBounty_BelowThreshold(t *testing.T) {
	t.Setenv("HUNT_NOTIFY_MIN_BOUNTY_USD", "250")
	b := hunt.Bounty{AmountCents: 24999} // $249.99
	assert.False(t, isUrgentBounty(b), "bounty at $249.99 must not be urgent (threshold $250)")
}

func TestIsUrgentBounty_ZeroAmount(t *testing.T) {
	t.Setenv("HUNT_NOTIFY_MIN_BOUNTY_USD", "250")
	b := hunt.Bounty{AmountCents: 0}
	assert.False(t, isUrgentBounty(b), "zero-amount bounty must not be urgent")
}

func TestIsUrgentSecurity_AboveThreshold(t *testing.T) {
	t.Setenv("HUNT_NOTIFY_MIN_SECURITY_USD", "5000")
	s := hunt.Security{MaxBounty: 5001}
	assert.True(t, isUrgentSecurity(s), "security at $5001 must be urgent (threshold $5000)")
}

func TestIsUrgentSecurity_BelowThreshold(t *testing.T) {
	t.Setenv("HUNT_NOTIFY_MIN_SECURITY_USD", "5000")
	s := hunt.Security{MaxBounty: 4999}
	assert.False(t, isUrgentSecurity(s), "security at $4999 must not be urgent")
}

func TestIsUrgentFreelance_AboveThreshold(t *testing.T) {
	t.Setenv("HUNT_NOTIFY_MIN_FREELANCE_USD", "2000")
	f := hunt.Freelance{BudgetMax: 2001}
	assert.True(t, isUrgentFreelance(f), "freelance at $2001 must be urgent")
}

func TestIsUrgentFreelance_BelowThreshold(t *testing.T) {
	t.Setenv("HUNT_NOTIFY_MIN_FREELANCE_USD", "2000")
	f := hunt.Freelance{BudgetMax: 1999}
	assert.False(t, isUrgentFreelance(f), "freelance at $1999 must not be urgent")
}

func TestIsUrgentFreelance_ZeroBudget(t *testing.T) {
	t.Setenv("HUNT_NOTIFY_MIN_FREELANCE_USD", "2000")
	f := hunt.Freelance{BudgetMax: 0}
	assert.False(t, isUrgentFreelance(f), "zero-budget freelance must never be urgent")
}

// --- Backfill guard: >threshold → exactly ONE summary card, zero individual ---

func TestApplyBountyNotifyPolicy_Backfill_OneSummary(t *testing.T) {
	t.Setenv("HUNT_NOTIFY_BACKFILL_THRESHOLD", "5")
	t.Setenv("HUNT_NOTIFY_MIN_BOUNTY_USD", "250")
	n := &fakeNotifier{}

	// 6 created items > threshold(5); all urgent so they'd normally send individually
	created := make([]hunt.Bounty, 6)
	for i := range created {
		created[i] = hunt.Bounty{AmountCents: 50000, Title: "Big Bug"}
	}

	applyBountyNotifyPolicy(n, created)

	require.Len(t, n.bounties, 1, "backfill: exactly ONE summary card must fire (not individual cards)")
	assert.Contains(t, n.bounties[0].Title, "6", "summary title must mention count")
	assert.Contains(t, n.bounties[0].Title, "bounty", "summary title must mention kind")
}

func TestApplySecurityNotifyPolicy_Backfill_OneSummary(t *testing.T) {
	t.Setenv("HUNT_NOTIFY_BACKFILL_THRESHOLD", "3")
	t.Setenv("HUNT_NOTIFY_MIN_SECURITY_USD", "100")
	n := &fakeNotifier{}

	created := make([]hunt.Security, 4)
	for i := range created {
		created[i] = hunt.Security{MaxBounty: 50000, Name: "BigProgram"}
	}

	applySecurityNotifyPolicy(n, created)

	require.Len(t, n.security, 1, "backfill: exactly ONE summary card must fire")
	assert.Contains(t, n.security[0].Name, "4", "summary title must mention count")
}

func TestApplyFreelanceNotifyPolicy_Backfill_OneSummary(t *testing.T) {
	t.Setenv("HUNT_NOTIFY_BACKFILL_THRESHOLD", "2")
	t.Setenv("HUNT_NOTIFY_MIN_FREELANCE_USD", "100")
	n := &fakeNotifier{}

	created := make([]hunt.Freelance, 3)
	for i := range created {
		created[i] = hunt.Freelance{BudgetMax: 5000, Title: "BigProject"}
	}

	applyFreelanceNotifyPolicy(n, created)

	require.Len(t, n.freelance, 1, "backfill: exactly ONE summary card must fire")
}

// --- Normal cycle: only urgent items get individual cards ---

func TestApplyBountyNotifyPolicy_NormalCycle_OnlyUrgent(t *testing.T) {
	t.Setenv("HUNT_NOTIFY_BACKFILL_THRESHOLD", "30")
	t.Setenv("HUNT_NOTIFY_MIN_BOUNTY_USD", "250")
	n := &fakeNotifier{}

	created := []hunt.Bounty{
		{AmountCents: 50000, Title: "High"},  // $500 — urgent
		{AmountCents: 10000, Title: "Low"},   // $100 — not urgent
		{AmountCents: 25000, Title: "Exact"}, // $250 — urgent (at threshold)
	}

	applyBountyNotifyPolicy(n, created)

	// Only 2 urgent items should fire; 1 below threshold suppressed.
	require.Len(t, n.bounties, 2, "only urgent bounties must get individual cards")
	titles := []string{n.bounties[0].Title, n.bounties[1].Title}
	assert.Contains(t, titles, "High")
	assert.Contains(t, titles, "Exact")
	assert.NotContains(t, titles, "Low", "below-threshold bounty must not notify")
}

func TestApplySecurityNotifyPolicy_NormalCycle_OnlyUrgent(t *testing.T) {
	t.Setenv("HUNT_NOTIFY_BACKFILL_THRESHOLD", "30")
	t.Setenv("HUNT_NOTIFY_MIN_SECURITY_USD", "5000")
	n := &fakeNotifier{}

	created := []hunt.Security{
		{MaxBounty: 10000, Name: "HVP"}, // urgent
		{MaxBounty: 1000, Name: "LVP"},  // not urgent
	}

	applySecurityNotifyPolicy(n, created)

	require.Len(t, n.security, 1, "only urgent security programs must notify")
	assert.Equal(t, "HVP", n.security[0].Name)
}

func TestApplyFreelanceNotifyPolicy_NormalCycle_OnlyUrgent(t *testing.T) {
	t.Setenv("HUNT_NOTIFY_BACKFILL_THRESHOLD", "30")
	t.Setenv("HUNT_NOTIFY_MIN_FREELANCE_USD", "2000")
	n := &fakeNotifier{}

	created := []hunt.Freelance{
		{BudgetMax: 5000, Title: "BigGig"},   // urgent
		{BudgetMax: 500, Title: "SmallGig"},  // not urgent
		{BudgetMax: 0, Title: "ZeroGig"},     // zero budget — never urgent
	}

	applyFreelanceNotifyPolicy(n, created)

	require.Len(t, n.freelance, 1, "only urgent freelance items must notify")
	assert.Equal(t, "BigGig", n.freelance[0].Title)
}

// --- Non-created (merged) items must not trigger any notify ---

func TestApplyBountyNotifyPolicy_NilNotifier_NoPanic(t *testing.T) {
	// Must not panic with nil notifier.
	applyBountyNotifyPolicy(nil, []hunt.Bounty{{AmountCents: 50000}})
}

func TestApplyBountyNotifyPolicy_EmptyCreated_NoNotify(t *testing.T) {
	n := &fakeNotifier{}
	applyBountyNotifyPolicy(n, nil)
	assert.Empty(t, n.bounties, "empty created list must not notify")
}

// --- Guard: if backfill fires, individual cards are ZERO (not double-send) ---

func TestApplyBountyNotifyPolicy_Backfill_NoDoubleNotify(t *testing.T) {
	t.Setenv("HUNT_NOTIFY_BACKFILL_THRESHOLD", "2")
	t.Setenv("HUNT_NOTIFY_MIN_BOUNTY_USD", "1") // very low threshold so all would be urgent
	n := &fakeNotifier{}

	// 5 items > threshold(2); ALL urgent; backfill guard must suppress individual cards
	created := make([]hunt.Bounty, 5)
	for i := range created {
		created[i] = hunt.Bounty{AmountCents: 10000, Title: "Bug"}
	}

	applyBountyNotifyPolicy(n, created)

	// Exactly 1 card (the summary), NOT 1+5
	assert.Len(t, n.bounties, 1, "backfill must send exactly ONE card — no double-send with individual cards")
}

// --- summaryTitle helper ---

func TestSummaryTitle_PluralCount(t *testing.T) {
	title := summaryTitle("bounty", 42, "AngelList")
	assert.Contains(t, title, "42")
	assert.Contains(t, title, "bounty")
	assert.Contains(t, title, "AngelList")
}

func TestSummaryTitle_SingleCount(t *testing.T) {
	title := summaryTitle("security", 1, "")
	assert.Contains(t, title, "1")
	assert.Contains(t, title, "item ingested")
}

// --- Regression: backfill test fails if guard is reverted ---
// This test explicitly verifies the RED condition: if you remove the backfill guard
// (replace with individual sends), len(n.bounties) == len(created) != 1 → test fails.

func TestApplyBountyNotifyPolicy_BackfillGuard_RedOnRevert(t *testing.T) {
	t.Setenv("HUNT_NOTIFY_BACKFILL_THRESHOLD", "2")
	t.Setenv("HUNT_NOTIFY_MIN_BOUNTY_USD", "0") // gate open so all would be urgent
	n := &fakeNotifier{}

	created := []hunt.Bounty{
		{AmountCents: 100, Title: "A"},
		{AmountCents: 200, Title: "B"},
		{AmountCents: 300, Title: "C"}, // 3 > threshold(2) → backfill summary
	}

	applyBountyNotifyPolicy(n, created)

	// If the guard is present: 1 summary card.
	// If the guard is reverted (naively sends individual cards): 3 cards → this assert FAILS.
	assert.Len(t, n.bounties, 1,
		"RED-on-revert: removing the backfill guard would send 3 individual cards — this assertion catches that regression")
}
