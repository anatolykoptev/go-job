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
// Items without an amount (AmountCents==0) are never urgent.
func isUrgentBounty(b hunt.Bounty) bool {
	minCents := int64(env.Int("HUNT_NOTIFY_MIN_BOUNTY_USD", defaultNotifyMinBountyUSD)) * 100
	if minCents <= 0 {
		return true
	}
	return b.AmountCents >= minCents
}

// isUrgentSecurity returns true if the security program warrants an individual Telegram card.
func isUrgentSecurity(s hunt.Security) bool {
	minUSD := env.Int("HUNT_NOTIFY_MIN_SECURITY_USD", defaultNotifyMinSecurityUSD)
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
	minUSD := env.Int("HUNT_NOTIFY_MIN_FREELANCE_USD", defaultNotifyMinFreelanceUSD)
	if minUSD <= 0 {
		return true
	}
	return f.BudgetMax >= minUSD
}

// notifyBackfillThreshold returns the per-kind threshold above which a single
// summary card is sent instead of individual cards.
func notifyBackfillThreshold() int {
	return env.Int("HUNT_NOTIFY_BACKFILL_THRESHOLD", defaultNotifyBackfillThreshold)
}

// applyBountyNotifyPolicy implements the shared notify policy for one ingest cycle
// over a set of created (genuinely-new, open) bounties. Called by persistBounties.
//
// Policy:
//   - len(created) > threshold → one summary card, no individual cards.
//   - else → individual card for each urgent created item (isUrgentBounty).
//   - Non-urgent created items are silently persisted (DB/UI); no card.
func applyBountyNotifyPolicy(notifier hunt.Notifier, created []hunt.Bounty) {
	if notifier == nil || len(created) == 0 {
		return
	}
	threshold := notifyBackfillThreshold()
	if len(created) > threshold {
		notifier.NotifyNewBounty(hunt.Bounty{
			Title:  summaryTitle("bounty", len(created), created[0].Title),
			URL:    "https://algora.io",
			Source: "ingest-summary",
		})
		slog.Info("hunt: bounty notify suppressed (backfill)", slog.Int("created", len(created)), slog.Int("threshold", threshold))
		return
	}
	for _, b := range created {
		if isUrgentBounty(b) {
			notifier.NotifyNewBounty(b)
		}
	}
}

// applySecurityNotifyPolicy implements the shared notify policy for security programs.
func applySecurityNotifyPolicy(notifier hunt.Notifier, created []hunt.Security) {
	if notifier == nil || len(created) == 0 {
		return
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
		return
	}
	for _, s := range created {
		if isUrgentSecurity(s) {
			notifier.NotifyNewSecurity(s)
		}
	}
}

// applyFreelanceNotifyPolicy implements the shared notify policy for freelance projects.
func applyFreelanceNotifyPolicy(notifier hunt.Notifier, created []hunt.Freelance) {
	if notifier == nil || len(created) == 0 {
		return
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
		return
	}
	for _, f := range created {
		if isUrgentFreelance(f) {
			notifier.NotifyNewFreelance(f)
		}
	}
}

// summaryTitle builds a one-line summary title for backfill summary cards.
// e.g. "🆕 42 new security programs ingested (top: AngelList VDP)"
func summaryTitle(kind string, count int, topName string) string {
	s := "🆕 " + itoa(count) + " new " + kind
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

func itoa(n int) string {
	return strconv.Itoa(n)
}

// fetchAllBountiesUnlimited fetches ALL bounties (no per-call cap) for scheduled ingest.
func FetchAllBountiesUnlimited(ctx context.Context) []engine.BountyListing {
	const bigLimit = 10000

	var all []engine.BountyListing

	if bvecs, err := SearchAlgoraEnriched(ctx, bigLimit); err == nil {
		for _, bv := range bvecs {
			all = append(all, bv.Bounty)
		}
	} else {
		slog.Warn("opp ingest: algora error", slog.Any("error", err))
	}

	sources := []struct {
		name string
		fn   func(context.Context, int) ([]engine.BountyListing, error)
	}{
		{"opire", SearchOpire},
		{"bountyhub", SearchBountyHub},
		{"boss", SearchBoss},
		{"lightning", SearchLightning},
		{"collaborators", SearchCollaborators},
	}

	for _, s := range sources {
		bounties, err := s.fn(ctx, bigLimit)
		if err != nil {
			slog.Warn("opp ingest: "+s.name+" error", slog.Any("error", err))
			continue
		}
		all = append(all, bounties...)
	}

	return all
}

// fetchAllSecurityUnlimited fetches ALL security programs (no cap) for scheduled ingest.
func FetchAllSecurityUnlimited(ctx context.Context) []engine.SecurityProgram {
	const bigLimit = 10000

	var all []engine.SecurityProgram

	btd, err := fetchAllSecurityPrograms(ctx) // fetches all 5 BTD sources, no limit
	if err != nil {
		slog.Warn("opp ingest: security btd error", slog.Any("error", err))
	} else {
		all = append(all, btd...)
	}

	imm, err := SearchImmunefi(ctx, bigLimit)
	if err != nil {
		slog.Warn("opp ingest: immunefi error", slog.Any("error", err))
	} else {
		all = append(all, imm...)
	}

	shr, err := SearchSherlock(ctx, bigLimit)
	if err != nil {
		slog.Warn("opp ingest: sherlock error", slog.Any("error", err))
	} else {
		all = append(all, shr...)
	}

	cantina, err := SearchCantina(ctx, bigLimit)
	if err != nil {
		slog.Warn("opp ingest: cantina error", slog.Any("error", err))
	} else {
		all = append(all, cantina...)
	}

	c4r, err := SearchCode4rena(ctx, bigLimit)
	if err != nil {
		slog.Warn("opp ingest: code4rena error", slog.Any("error", err))
	} else {
		all = append(all, c4r...)
	}

	return all
}

// fetchAllFreelanceUnlimited fetches ALL freelance items (no cap) for scheduled ingest.
func FetchAllFreelanceUnlimited(ctx context.Context) []engine.FreelanceJob {
	const bigLimit = 10000

	var all []engine.FreelanceJob

	rok, err := SearchRemoteOKFreelance(ctx, langAliasGolang, bigLimit)
	if err != nil {
		slog.Warn("opp ingest: remoteok error", slog.Any("error", err))
	} else {
		all = append(all, rok...)
	}

	him, err := SearchHimalayas(ctx, langAliasGolang, bigLimit)
	if err != nil {
		slog.Warn("opp ingest: himalayas error", slog.Any("error", err))
	} else {
		all = append(all, him...)
	}

	return all
}
