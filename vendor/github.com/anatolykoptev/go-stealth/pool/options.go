package pool

import (
	"math"
	"math/rand"
	"time"
)

// Config holds pool configuration parameters.
type Config struct {
	// AlertHook is called when notable events occur (account deactivated, proxy down, etc.).
	// topic is a dot-separated event name, payload is event-specific data.
	AlertHook func(topic string, payload any)

	// ProxyBackoff configures exponential backoff for proxy failures.
	ProxyBackoff BackoffConfig

	// NonResponsiveBackoff configures exponential backoff for items that hit the
	// consecutive-failure trip threshold against a transiently-broken endpoint.
	// Consumers pass the per-item trip count to SoftDeactivateBackoff so a flapping
	// endpoint produces a growing-but-capped cooldown instead of a permanent latch.
	NonResponsiveBackoff BackoffConfig
}

// BackoffConfig defines exponential backoff parameters.
type BackoffConfig struct {
	InitialWait time.Duration
	MaxWait     time.Duration
	Multiplier  float64
	JitterPct   float64
}

// Duration returns the cooldown for the given trip number (1-indexed).
// trip 1 yields the base InitialWait; each subsequent trip multiplies by
// Multiplier up to MaxWait, with +/- JitterPct random variation applied to
// spread simultaneous re-admissions (thundering-herd mitigation). A trip <= 0
// is treated as trip 1. This mirrors the root stealth.BackoffConfig.Duration
// helper but is keyed on trip count rather than a 0-indexed attempt.
func (b BackoffConfig) Duration(trip int) time.Duration {
	attempt := trip - 1
	if attempt < 0 {
		attempt = 0
	}
	base := float64(b.InitialWait) * math.Pow(b.Multiplier, float64(attempt))
	if base > float64(b.MaxWait) {
		base = float64(b.MaxWait)
	}
	jitter := base * b.JitterPct * (2*rand.Float64() - 1)
	return max(time.Duration(base+jitter), 0)
}

// DefaultProxyBackoff is the default proxy error backoff configuration.
var DefaultProxyBackoff = BackoffConfig{
	InitialWait: 30 * time.Second,
	MaxWait:     30 * time.Minute,
	Multiplier:  2.0,
	JitterPct:   0.3,
}

// DefaultNonResponsiveBackoff is the default backoff for items that trip the
// consecutive-failure threshold (e.g. a transiently-broken upstream endpoint).
// base 5m, x2, cap 30m, +/-30% jitter: trip1~5m, trip2~10m, trip3~20m, trip4+~30m.
// Jitter is applied AFTER the cap, so a capped cooldown can land anywhere in
// ~21..39m -- the spread is deliberate (thundering-herd mitigation on simultaneous
// re-admission). The 30m cap mirrors twscrape/Envoy "eventually always retried"
// behaviour so a recovered endpoint self-heals within ~one cap window without
// operator intervention.
var DefaultNonResponsiveBackoff = BackoffConfig{
	InitialWait: 5 * time.Minute,
	MaxWait:     30 * time.Minute,
	Multiplier:  2.0,
	JitterPct:   0.3,
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		ProxyBackoff:         DefaultProxyBackoff,
		NonResponsiveBackoff: DefaultNonResponsiveBackoff,
	}
}
