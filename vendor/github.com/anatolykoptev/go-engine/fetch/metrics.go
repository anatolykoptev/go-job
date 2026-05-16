package fetch

import (
	"github.com/prometheus/client_golang/prometheus"
)

// fetchMetrics is the internal interface used by fetchDirectFirst to record
// observability events. The default implementation (promTierMetrics) writes to
// Prometheus. Tests inject a lightweight in-process stub via withMetrics.
//
// Keeping the interface internal means consumers do not depend on prometheus
// unless they wire up the default metrics themselves via InitMetrics.
type fetchMetrics interface {
	// incTier records a completed fetch for the given tier and result.
	// tier: "direct" | "proxy"
	// result: "ok" | "err"
	incTier(tier, result string)

	// incBlockSignal records a block signal detected on direct tier.
	// signal: "hard" | "soft" | "tls"
	incBlockSignal(signal string)

	// incEscalation records a direct→proxy escalation and its reason.
	// reason: "hard" | "soft" | "tls" | "domain_hint" | "cached"
	incEscalation(reason string)

	// setBlockCacheHosts updates the current size of the direct-block FIFO cache.
	setBlockCacheHosts(n int)
}

// noopMetrics is a no-op implementation — used when WithMetrics is not called.
type noopMetrics struct{}

func (noopMetrics) incTier(_, _ string)     {}
func (noopMetrics) incBlockSignal(_ string)  {}
func (noopMetrics) incEscalation(_ string)   {}
func (noopMetrics) setBlockCacheHosts(_ int) {}

// promTierMetrics implements fetchMetrics using prometheus counter/gauge vectors.
type promTierMetrics struct {
	tierTotal     *prometheus.CounterVec
	blockSignal   *prometheus.CounterVec
	escalations   *prometheus.CounterVec
	blockCacheSize prometheus.Gauge
}

// NewPromMetrics creates a promTierMetrics registered on the given prometheus.Registerer.
// Pass prometheus.DefaultRegisterer for production use.
//
// Metric names follow the go_engine_fetch_ prefix:
//
//	go_engine_fetch_tier_total{tier, result}
//	go_engine_fetch_block_signal_total{signal}
//	go_engine_fetch_proxy_escalations_total{reason}
//	go_engine_fetch_direct_block_cache_hosts
func NewPromMetrics(reg prometheus.Registerer) (fetchMetrics, error) {
	tierTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "go_engine_fetch_tier_total",
		Help: "Number of fetches per tier (direct|proxy) and result (ok|err).",
	}, []string{"tier", "result"})

	blockSignal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "go_engine_fetch_block_signal_total",
		Help: "Block signals detected on the direct tier, classified by type (hard|soft|tls).",
	}, []string{"signal"})

	escalations := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "go_engine_fetch_proxy_escalations_total",
		Help: "Direct→proxy escalations by reason (hard|soft|tls|domain_hint|cached).",
	}, []string{"reason"})

	blockCacheSize := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "go_engine_fetch_direct_block_cache_hosts",
		Help: "Current number of hosts in the direct-block FIFO cache.",
	})

	for _, c := range []prometheus.Collector{tierTotal, blockSignal, escalations, blockCacheSize} {
		if err := reg.Register(c); err != nil {
			return nil, err
		}
	}

	return &promTierMetrics{
		tierTotal:      tierTotal,
		blockSignal:    blockSignal,
		escalations:    escalations,
		blockCacheSize: blockCacheSize,
	}, nil
}

func (m *promTierMetrics) incTier(tier, result string) {
	m.tierTotal.WithLabelValues(tier, result).Inc()
}

func (m *promTierMetrics) incBlockSignal(signal string) {
	m.blockSignal.WithLabelValues(signal).Inc()
}

func (m *promTierMetrics) incEscalation(reason string) {
	m.escalations.WithLabelValues(reason).Inc()
}

func (m *promTierMetrics) setBlockCacheHosts(n int) {
	m.blockCacheSize.Set(float64(n))
}
