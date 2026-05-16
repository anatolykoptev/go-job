package metrics

import (
	"context"
	"log/slog"
	"time"

	kitmetrics "github.com/anatolykoptev/go-kit/metrics"
)

const slowThreshold = 5 * time.Second

// TrackCall is a nil-safe call+error counter pair.
// Delegates to go-kit/metrics.TrackCall.
func TrackCall(reg *Registry, callName, errName string, fn func() error) error {
	return kitmetrics.TrackCall(reg, callName, errName, fn)
}

// TrackOperation logs a warning if fn takes longer than 5 seconds.
func TrackOperation(ctx context.Context, name string, fn func(context.Context) error) error {
	start := time.Now()
	err := fn(ctx)
	elapsed := time.Since(start)
	if elapsed > slowThreshold {
		slog.WarnContext(ctx, "slow operation",
			slog.String("op", name),
			slog.Duration("elapsed", elapsed),
		)
	}
	return err
}
