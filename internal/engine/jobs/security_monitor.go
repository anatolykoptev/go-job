package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/anatolykoptev/go_job/internal/engine"
)

const securitySeenIDsKey = "security_seen_ids"

// StartSecurityMonitor launches a background goroutine that polls for new
// security bounty programs and sends Telegram notifications via vaelor.
func StartSecurityMonitor(ctx context.Context) {
	interval := 30 * time.Minute

	if engine.Cfg.VaelorNotifyURL == "" {
		slog.Info("security_monitor: disabled (VAELOR_NOTIFY_URL not set)")
		return
	}

	slog.Info("security_monitor: starting", slog.Duration("interval", interval))

	// Initial run after 45s delay (let caches warm up).
	time.AfterFunc(45*time.Second, func() {
		checkNewSecurityPrograms(ctx)
	})

	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				slog.Info("security_monitor: stopped")
				return
			case <-ticker.C:
				checkNewSecurityPrograms(ctx)
			}
		}
	}()
}

func checkNewSecurityPrograms(ctx context.Context) {
	var programs []engine.SecurityProgram

	secProgs, err := SearchSecurityPrograms(ctx, 500)
	if err != nil {
		slog.Warn("security_monitor: security fetch failed", slog.Any("error", err))
	}
	programs = append(programs, secProgs...)

	immunefiProgs, imErr := SearchImmunefi(ctx, 500)
	if imErr != nil {
		slog.Warn("security_monitor: immunefi fetch failed", slog.Any("error", imErr))
	}
	programs = append(programs, immunefiProgs...)

	if len(programs) == 0 {
		if err != nil || imErr != nil {
			slog.Warn("security_monitor: all sources failed")
		}
		return
	}

	// Load previously seen IDs from cache.
	seenIDs, _ := engine.CacheLoadJSON[map[string]bool](ctx, securitySeenIDsKey)
	if seenIDs == nil {
		// First run — store all current IDs without notifying.
		seenIDs = make(map[string]bool, len(programs))
		for _, p := range programs {
			seenIDs[p.URL] = true
		}
		engine.CacheStoreJSON(ctx, securitySeenIDsKey, "", seenIDs)
		slog.Info("security_monitor: initialized seen set",
			slog.Int("count", len(seenIDs)))
		return
	}

	// Find new programs.
	var newPrograms []engine.SecurityProgram
	for _, p := range programs {
		if !seenIDs[p.URL] {
			newPrograms = append(newPrograms, p)
			seenIDs[p.URL] = true
		}
	}

	if len(newPrograms) == 0 {
		return
	}

	// Update seen set.
	engine.CacheStoreJSON(ctx, securitySeenIDsKey, "", seenIDs)

	// Send notification for each new program.
	for _, p := range newPrograms {
		msg := formatSecurityNotification(p)
		if nErr := SendTelegramNotification(ctx, msg); nErr != nil {
			slog.Warn("security_monitor: notify failed",
				slog.Any("error", nErr), slog.String("url", p.URL))
		} else {
			slog.Info("security_monitor: notified", slog.String("url", p.URL))
		}
	}
}

func formatSecurityNotification(p engine.SecurityProgram) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("New Security Program [%s]\n", p.Platform))
	sb.WriteString(fmt.Sprintf("%s\n", p.Name))
	if p.MaxBounty != "" && p.MaxBounty != "$0" {
		sb.WriteString(fmt.Sprintf("Max bounty: %s\n", p.MaxBounty))
	}
	if len(p.Targets) > 0 {
		limit := 3
		if len(p.Targets) < limit {
			limit = len(p.Targets)
		}
		sb.WriteString(fmt.Sprintf("Scope: %s\n", strings.Join(p.Targets[:limit], ", ")))
	}
	sb.WriteString(p.URL)
	return sb.String()
}
