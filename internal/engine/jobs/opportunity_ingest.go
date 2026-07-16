package jobs

import (
	"context"
	"log/slog"
	"strconv"

	"github.com/anatolykoptev/go-kit/env"
	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/hunt"
)

// Notify-policy defaults. All are read from env per cycle so the operator
// can tune without a redeploy.
const (
	defaultNotifyBackfillThreshold = 30   // per-kind: if >N created in one cycle, send summary instead of individual cards
	defaultNotifyMinBountyUSD      = 250  // USD, bounties below this amount are not individually notified
	defaultNotifyMinSecurityUSD    = 5000 // USD, security programs with max_bounty below this are not individually notified
	defaultNotifyMinFreelanceUSD   = 2000 // USD, freelance with budget below this are not individually notified
)

// isUrgentBounty returns true if the bounty warrants an individual Telegram card.
// Items without an amount (AmountCents==0) are never urgent regardless of the threshold.
func isUrgentBounty(b hunt.Bounty) bool {
	if b.AmountCents == 0 {
		return false
	}
	minCents := int64(env.MustInt("HUNT_NOTIFY_MIN_BOUNTY_USD", defaultNotifyMinBountyUSD)) * 100
	if minCents <= 0 {
		return true
	}
	return b.AmountCents >= minCents
}

// isUrgentSecurity returns true if the security program warrants an individual Telegram card.
// Programs with no/zero max bounty are never urgent regardless of the threshold.
// Note: federacy programs carry no bounty-amount fields (only offers_awards flag) so
// their MaxBounty is always 0 after parsing — they never pass this gate and surface
// only via the DB + backfill summary card. This is an upstream-data limitation, not a
// defect; gating all bug_bounty programs as urgent would bypass the flood control.
func isUrgentSecurity(s hunt.Security) bool {
	if s.MaxBounty == 0 {
		return false
	}
	minUSD := env.MustInt("HUNT_NOTIFY_MIN_SECURITY_USD", defaultNotifyMinSecurityUSD)
	if minUSD <= 0 {
		return true
	}
	return s.MaxBounty >= minUSD
}

// isUrgentFreelance returns true if the freelance project warrants an individual Telegram card.
// Projects with no/zero budget are never urgent.
func isUrgentFreelance(f hunt.Freelance) bool {
	if f.BudgetMax == 0 {
		return false
	}
	minUSD := env.MustInt("HUNT_NOTIFY_MIN_FREELANCE_USD", defaultNotifyMinFreelanceUSD)
	if minUSD <= 0 {
		return true
	}
	return f.BudgetMax >= minUSD
}

// notifyBackfillThreshold returns the per-kind threshold above which a single
// summary card is sent instead of individual cards.
func notifyBackfillThreshold() int {
	return env.MustInt("HUNT_NOTIFY_BACKFILL_THRESHOLD", defaultNotifyBackfillThreshold)
}

// applyBountyNotifyPolicy implements the shared notify policy for one ingest cycle
// over a set of created (genuinely-new, open) bounties. Called by PersistBounties.
//
// Policy:
//   - len(created) > threshold → one summary card, no individual cards.
//   - else → individual card for each urgent created item (isUrgentBounty).
//   - Non-urgent created items are silently persisted (DB/UI); no card.
//
// Returns (notified, suppressed) counts as the single source of truth for the
// caller's log line; avoids re-deriving the policy a second time in PersistBounties.
func applyBountyNotifyPolicy(notifier hunt.Notifier, created []hunt.Bounty) (notified, suppressed int) {
	if notifier == nil || len(created) == 0 {
		return 0, 0
	}
	threshold := notifyBackfillThreshold()
	if len(created) > threshold {
		notifier.NotifyNewBounty(hunt.Bounty{
			Title:  summaryTitle("bounty", len(created), created[0].Title),
			URL:    "https://algora.io",
			Source: "ingest-summary",
		})
		slog.Info("hunt: bounty notify suppressed (backfill)", slog.Int("created", len(created)), slog.Int("threshold", threshold))
		return 0, len(created)
	}
	for _, b := range created {
		if isUrgentBounty(b) {
			notifier.NotifyNewBounty(b)
			notified++
		} else {
			suppressed++
		}
	}
	return notified, suppressed
}

// applySecurityNotifyPolicy implements the shared notify policy for security programs.
// Returns (notified, suppressed) counts.
func applySecurityNotifyPolicy(notifier hunt.Notifier, created []hunt.Security) (notified, suppressed int) {
	if notifier == nil || len(created) == 0 {
		return 0, 0
	}
	threshold := notifyBackfillThreshold()
	if len(created) > threshold {
		firstName := ""
		if len(created) > 0 {
			firstName = created[0].Name
		}
		notifier.NotifyNewSecurity(hunt.Security{
			Name:     summaryTitle("security", len(created), firstName),
			URL:      "https://github.com/arkadiyt/bounty-targets-data",
			Platform: "multi",
		})
		slog.Info("hunt: security notify suppressed (backfill)", slog.Int("created", len(created)), slog.Int("threshold", threshold))
		return 0, len(created)
	}
	for _, s := range created {
		if isUrgentSecurity(s) {
			notifier.NotifyNewSecurity(s)
			notified++
		} else {
			suppressed++
		}
	}
	return notified, suppressed
}

// applyFreelanceNotifyPolicy implements the shared notify policy for freelance projects.
// Returns (notified, suppressed) counts.
func applyFreelanceNotifyPolicy(notifier hunt.Notifier, created []hunt.Freelance) (notified, suppressed int) {
	if notifier == nil || len(created) == 0 {
		return 0, 0
	}
	threshold := notifyBackfillThreshold()
	if len(created) > threshold {
		firstName := ""
		if len(created) > 0 {
			firstName = created[0].Title
		}
		notifier.NotifyNewFreelance(hunt.Freelance{
			Title:    summaryTitle("freelance", len(created), firstName),
			URL:      "https://remoteok.com",
			Platform: "multi",
			Source:   "ingest-summary",
		})
		slog.Info("hunt: freelance notify suppressed (backfill)", slog.Int("created", len(created)), slog.Int("threshold", threshold))
		return 0, len(created)
	}
	for _, f := range created {
		if isUrgentFreelance(f) {
			notifier.NotifyNewFreelance(f)
			notified++
		} else {
			suppressed++
		}
	}
	return notified, suppressed
}

// summaryTitle builds a one-line summary title for backfill summary cards.
// e.g. "🆕 42 new security programs ingested (top: AngelList VDP)"
func summaryTitle(kind string, count int, topName string) string {
	s := "🆕 " + strconv.Itoa(count) + " new " + kind
	if count == 1 {
		s += " item ingested"
	} else {
		s += " items ingested"
	}
	if topName != "" {
		s += " (top: " + topName + ")"
	}
	return s
}

// FetchAllBountiesUnlimited fetches ALL bounties (no per-call cap) for scheduled ingest.
// Delegates to fetchAllBountiesImpl with a large limit and no total-result cap.
func FetchAllBountiesUnlimited(ctx context.Context) []engine.BountyListing {
	return fetchAllBountiesImpl(ctx, 10000, false)
}

// FetchAllSecurityUnlimited fetches ALL security programs (no cap) for scheduled ingest.
// Delegates to fetchAllSecurityImpl with a large limit and no total-result cap;
// applyCap=false also bypasses the result cache to pull the full live dataset.
func FetchAllSecurityUnlimited(ctx context.Context) []engine.SecurityProgram {
	return fetchAllSecurityImpl(ctx, 10000, false)
}

// FetchAllFreelanceUnlimited fetches ALL freelance items (no cap) for scheduled ingest.
// Delegates to fetchAllFreelanceImpl with a large limit and no total-result cap.
func FetchAllFreelanceUnlimited(ctx context.Context) []engine.FreelanceJob {
	return fetchAllFreelanceImpl(ctx, 10000, false)
}
