package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/anatolykoptev/go_job/internal/engine"
)

const (
	freelanceSeenKey      = "freelance_seen_ids"
	freelanceInterval     = 30 * time.Minute
	freelanceInitialDelay = 60 * time.Second
)

// StartFreelanceMonitor launches a background goroutine that polls RemoteOK
// and Himalayas for new freelance/remote jobs and sends Telegram notifications.
func StartFreelanceMonitor(ctx context.Context) {
	if engine.Cfg.VaelorNotifyURL == "" {
		slog.Info("freelance_monitor: disabled (VAELOR_NOTIFY_URL not set)")
		return
	}

	slog.Info("freelance_monitor: starting",
		slog.Duration("interval", freelanceInterval))

	time.AfterFunc(freelanceInitialDelay, func() {
		checkNewFreelanceJobs(ctx)
	})

	ticker := time.NewTicker(freelanceInterval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				slog.Info("freelance_monitor: stopped")
				return
			case <-ticker.C:
				checkNewFreelanceJobs(ctx)
			}
		}
	}()
}

func checkNewFreelanceJobs(ctx context.Context) {
	var allJobs []engine.FreelanceJob

	// RemoteOK: multiple tags.
	for _, tag := range []string{"golang", "devops", "security"} {
		jobs, err := SearchRemoteOKFreelance(ctx, tag, 50)
		if err != nil {
			slog.Warn("freelance_monitor: remoteok fetch failed",
				slog.String("tag", tag), slog.Any("error", err))
			continue
		}
		allJobs = append(allJobs, jobs...)
	}

	// Himalayas: golang query.
	hJobs, err := SearchHimalayas(ctx, "golang", 50)
	if err != nil {
		slog.Warn("freelance_monitor: himalayas fetch failed",
			slog.Any("error", err))
	}
	allJobs = append(allJobs, hJobs...)

	if len(allJobs) == 0 {
		slog.Debug("freelance_monitor: no jobs found from any source")
		return
	}

	// Load previously seen URLs from cache.
	seenURLs, _ := engine.CacheLoadJSON[map[string]bool](ctx, freelanceSeenKey)
	if seenURLs == nil {
		// First run — store all current URLs without notifying.
		seenURLs = make(map[string]bool, len(allJobs))
		for _, j := range allJobs {
			seenURLs[j.URL] = true
		}
		engine.CacheStoreJSON(ctx, freelanceSeenKey, "", seenURLs)
		slog.Info("freelance_monitor: initialized seen set",
			slog.Int("count", len(seenURLs)))
		return
	}

	// Find new jobs.
	var newJobs []engine.FreelanceJob
	for _, j := range allJobs {
		if !seenURLs[j.URL] {
			newJobs = append(newJobs, j)
			seenURLs[j.URL] = true
		}
	}

	if len(newJobs) == 0 {
		return
	}

	// Update seen set.
	engine.CacheStoreJSON(ctx, freelanceSeenKey, "", seenURLs)

	// Send notifications.
	for _, j := range newJobs {
		msg := formatFreelanceNotification(j)
		if nErr := SendTelegramNotification(ctx, msg); nErr != nil {
			slog.Warn("freelance_monitor: notify failed",
				slog.Any("error", nErr), slog.String("url", j.URL))
		} else {
			slog.Info("freelance_monitor: notified", slog.String("url", j.URL))
		}
	}
}

func formatFreelanceNotification(j engine.FreelanceJob) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[%s] %s\n", j.Source, j.Title))
	if j.Company != "" {
		sb.WriteString(fmt.Sprintf("Company: %s\n", j.Company))
	}
	if j.SalaryMin > 0 || j.SalaryMax > 0 {
		sb.WriteString(fmt.Sprintf("Salary: $%d–$%d\n", j.SalaryMin, j.SalaryMax))
	}
	if len(j.Tags) > 0 {
		sb.WriteString(fmt.Sprintf("Tags: %s\n", strings.Join(j.Tags, ", ")))
	}
	sb.WriteString(j.URL)
	return sb.String()
}
