package pool

import "time"

// Config holds pool configuration parameters.
type Config struct {
	// AlertHook is called when notable events occur (account deactivated, proxy down, etc.).
	// topic is a dot-separated event name, payload is event-specific data.
	AlertHook func(topic string, payload any)

	// ProxyBackoff configures exponential backoff for proxy failures.
	ProxyBackoff BackoffConfig
}

// BackoffConfig defines exponential backoff parameters.
type BackoffConfig struct {
	InitialWait time.Duration
	MaxWait     time.Duration
	Multiplier  float64
	JitterPct   float64
}

// DefaultProxyBackoff is the default proxy error backoff configuration.
var DefaultProxyBackoff = BackoffConfig{
	InitialWait: 30 * time.Second,
	MaxWait:     30 * time.Minute,
	Multiplier:  2.0,
	JitterPct:   0.3,
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		ProxyBackoff: DefaultProxyBackoff,
	}
}
