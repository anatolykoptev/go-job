package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/anatolykoptev/go_job/internal/engine"
)

const bountySeenIDsKey = "bounty_seen_ids"

// StartBountyMonitor launches a background goroutine that polls for new bounties
// and sends Telegram notifications via vaelor.
func StartBountyMonitor(ctx context.Context) {
	interval := engine.Cfg.BountyMonitorInterval
	if interval <= 0 {
		interval = 15 * time.Minute
	}

	if engine.Cfg.VaelorNotifyURL == "" {
		slog.Info("bounty_monitor: disabled (VAELOR_NOTIFY_URL not set)")
		return
	}

	slog.Info("bounty_monitor: starting", slog.Duration("interval", interval))

	// Initial run after short delay (let caches warm up).
	time.AfterFunc(30*time.Second, func() {
		checkNewBounties(ctx)
	})

	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				slog.Info("bounty_monitor: stopped")
				return
			case <-ticker.C:
				checkNewBounties(ctx)
			}
		}
	}()
}

func checkNewBounties(ctx context.Context) {
	bounties, err := searchAlgoraAPI(ctx, 50)
	if err != nil {
		slog.Warn("bounty_monitor: algora fetch failed", slog.Any("error", err))
	}

	// Also fetch Opire bounties and merge.
	opireBounties, opireErr := SearchOpire(ctx, 50)
	if opireErr != nil {
		slog.Warn("bounty_monitor: opire fetch failed", slog.Any("error", opireErr))
	}
	bounties = append(bounties, opireBounties...)

	// Also fetch BountyHub bounties and merge.
	bhBounties, bhErr := SearchBountyHub(ctx, 50)
	if bhErr != nil {
		slog.Warn("bounty_monitor: bountyhub fetch failed", slog.Any("error", bhErr))
	}
	bounties = append(bounties, bhBounties...)

	// Also fetch Boss.dev bounties and merge.
	bossBounties, bossErr := SearchBoss(ctx, 50)
	if bossErr != nil {
		slog.Warn("bounty_monitor: boss fetch failed", slog.Any("error", bossErr))
	}
	bounties = append(bounties, bossBounties...)

	// Also fetch Lightning Bounties and merge.
	lnBounties, lnErr := SearchLightning(ctx, 50)
	if lnErr != nil {
		slog.Warn("bounty_monitor: lightning fetch failed", slog.Any("error", lnErr))
	}
	bounties = append(bounties, lnBounties...)

	// Also fetch Collaborators.build bounties and merge.
	collabBounties, collabErr := SearchCollaborators(ctx, 50)
	if collabErr != nil {
		slog.Warn("bounty_monitor: collaborators fetch failed", slog.Any("error", collabErr))
	}
	bounties = append(bounties, collabBounties...)

	if len(bounties) == 0 {
		if err != nil || opireErr != nil || bhErr != nil || bossErr != nil || lnErr != nil || collabErr != nil {
			slog.Warn("bounty_monitor: all sources failed")
		}
		return
	}

	// Load previously seen IDs from cache.
	seenIDs, _ := engine.CacheLoadJSON[map[string]bool](ctx, bountySeenIDsKey)
	if seenIDs == nil {
		// First run — store all current IDs without notifying.
		seenIDs = make(map[string]bool, len(bounties))
		for _, b := range bounties {
			seenIDs[b.URL] = true
		}
		engine.CacheStoreJSON(ctx, bountySeenIDsKey, "", seenIDs)
		slog.Info("bounty_monitor: initialized seen set", slog.Int("count", len(seenIDs)))
		return
	}

	// Find new bounties.
	var newBounties []engine.BountyListing
	for _, b := range bounties {
		if !seenIDs[b.URL] {
			newBounties = append(newBounties, b)
			seenIDs[b.URL] = true
		}
	}

	if len(newBounties) == 0 {
		return
	}

	// Update seen set.
	engine.CacheStoreJSON(ctx, bountySeenIDsKey, "", seenIDs)

	// Send notification for each new bounty.
	for _, b := range newBounties {
		msg := formatBountyNotification(b)
		if err := SendTelegramNotification(ctx, msg); err != nil {
			slog.Warn("bounty_monitor: notify failed", slog.Any("error", err), slog.String("url", b.URL))
		} else {
			slog.Info("bounty_monitor: notified", slog.String("url", b.URL))
		}
	}
}

func formatBountyNotification(b engine.BountyListing) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("New Bounty %s\n", b.Amount))
	sb.WriteString(fmt.Sprintf("%s\n", b.Title))
	if len(b.Skills) > 0 {
		sb.WriteString(fmt.Sprintf("Skills: %s\n", strings.Join(b.Skills, ", ")))
	}
	sb.WriteString(b.URL)
	return sb.String()
}
