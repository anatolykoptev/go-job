package search

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/anatolykoptev/go-stealth/ratelimit"

	"github.com/anatolykoptev/go-engine/sources"
)

const (
	// defaultPaceMinMS is the minimum delay between consecutive requests to the
	// same search engine. Conservative enough to fall within human-browsing pace.
	defaultPaceMinMS = 1000
	// defaultPaceJitterMS is the upper bound of the uniform random jitter added
	// on top of defaultPaceMinMS. Realized spacing = [1s, 2.5s).
	defaultPaceJitterMS = 1500
)

// NewScraperPacer constructs a KeyedPacer from environment variables:
//
//	SCRAPER_PACE_MIN_MS   — minimum delay between same-engine requests (ms);
//	                        defaults to 1000 when unset.
//	SCRAPER_PACE_JITTER_MS — additional random jitter [0, N) ms; defaults to
//	                        1500 when unset. Set both to 0 to disable pacing.
//
// The returned pacer never delays the first request for any engine key (KeyedPacer
// first-hit semantics). Only repeated hits to the same engine within the spacing
// window are held back, making multi-query bursts human-paced while a single-query
// fan-out (one hit per engine) runs without any artificial delay.
//
// Pass ratelimit.WithPacerClock to inject a synthetic clock in tests.
func NewScraperPacer(opts ...ratelimit.PacerOption) *ratelimit.KeyedPacer {
	minMS := envIntDefault("SCRAPER_PACE_MIN_MS", defaultPaceMinMS)
	jitterMS := envIntDefault("SCRAPER_PACE_JITTER_MS", defaultPaceJitterMS)
	return ratelimit.NewKeyedPacer(
		time.Duration(minMS)*time.Millisecond,
		time.Duration(jitterMS)*time.Millisecond,
		opts...,
	)
}

// applyPacing wraps each enabled job's fn with a pacer.Wait call keyed by the
// job's engine label. Distinct engines never block each other (independent keys);
// only repeated requests to the same engine within a burst are spaced. When pacer
// is nil, jobs are left unchanged. Called by SearchDirect after the jobs slice is
// built, before goroutines are spawned.
func applyPacing(jobs []directJob, pacer *ratelimit.KeyedPacer) {
	if pacer == nil {
		return
	}
	for i := range jobs {
		if !jobs[i].enabled {
			continue
		}
		label, inner := jobs[i].label, jobs[i].fn
		jobs[i].fn = func(ctx context.Context) ([]sources.Result, error) {
			if err := pacer.Wait(ctx, label); err != nil {
				slog.Debug("scraper pacer wait cancelled",
					slog.String("engine", label),
					slog.Any("error", err))
				return nil, nil //nolint:nilerr // pacer cancelled: skip engine gracefully
			}
			return inner(ctx)
		}
	}
}

// envIntDefault reads an environment variable as a non-negative integer.
// Returns defaultVal when the variable is unset, empty, negative, or
// unparseable.
func envIntDefault(name string, defaultVal int) int {
	s := os.Getenv(name)
	if s == "" {
		return defaultVal
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < 0 {
		return defaultVal
	}
	return v
}
