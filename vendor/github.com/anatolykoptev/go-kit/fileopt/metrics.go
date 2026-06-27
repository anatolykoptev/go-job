package fileopt

import (
	"net/http"
	"time"

	"github.com/anatolykoptev/go-kit/metrics"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Optimization subprocess stages. Each stage is tracked independently so
// metrics show the marginal contribution of stage-2 tools (e.g. qpdf after
// gs) rather than only the combined ratio.
const (
	StageGS     = "gs"
	StageQPDF   = "qpdf"
	StageOxiPNG = "oxipng"
	StageCwebp  = "cwebp"
)

// Outcome labels for calls_total.
const (
	ResultSuccess = "success"
	ResultSkipped = "skipped" // binary missing or content-aware early-exit
	ResultError   = "error"   // subprocess failed
)

var (
	callsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "gokit",
			Subsystem: "fileopt",
			Name:      "calls_total",
			Help:      "Total number of optimization calls per stage and result.",
		},
		[]string{"stage", "result"},
	)

	durationSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "gokit",
			Subsystem: "fileopt",
			Name:      "duration_seconds",
			Help:      "Wall-clock duration of each optimization stage in seconds.",
			Buckets:   []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
		},
		[]string{"stage"},
	)

	ratioHist = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "gokit",
			Subsystem: "fileopt",
			Name:      "ratio",
			Help:      "bytes_after / bytes_before ratio per stage. Values <1 mean reduction; >1 means the stage made the file bigger.",
			Buckets:   []float64{0.3, 0.5, 0.7, 0.8, 0.9, 0.95, 0.99, 1.0, 1.05, 1.2},
		},
		[]string{"stage"},
	)

	bytesBefore = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "gokit",
			Subsystem: "fileopt",
			Name:      "bytes_before_total",
			Help:      "Cumulative bytes presented to each stage.",
		},
		[]string{"stage"},
	)

	bytesAfter = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "gokit",
			Subsystem: "fileopt",
			Name:      "bytes_after_total",
			Help:      "Cumulative bytes returned by each stage on success.",
		},
		[]string{"stage"},
	)
)

// RecordSuccess observes a successful optimization attempt.
func RecordSuccess(stage string, bytesIn, bytesOut int, dur time.Duration) {
	callsTotal.WithLabelValues(stage, ResultSuccess).Inc()
	durationSeconds.WithLabelValues(stage).Observe(dur.Seconds())
	bytesBefore.WithLabelValues(stage).Add(float64(bytesIn))
	bytesAfter.WithLabelValues(stage).Add(float64(bytesOut))
	if bytesIn > 0 {
		ratioHist.WithLabelValues(stage).Observe(float64(bytesOut) / float64(bytesIn))
	}
}

// RecordSkipped observes a call that was skipped because the binary was
// unavailable OR content-aware routing determined the stage was not needed
// (e.g. pdfHasImages=false → gs skip).
func RecordSkipped(stage string) {
	callsTotal.WithLabelValues(stage, ResultSkipped).Inc()
}

// RecordError observes a failed optimization attempt (subprocess non-zero exit).
func RecordError(stage string, dur time.Duration) {
	callsTotal.WithLabelValues(stage, ResultError).Inc()
	durationSeconds.WithLabelValues(stage).Observe(dur.Seconds())
}

// MetricsHandler returns the Prometheus /metrics exposition handler.
//
// Deprecated: use metrics.MetricsHandler — single exported handler for the
// whole go-kit module. Will be removed in the next major version.
func MetricsHandler() http.Handler {
	return metrics.MetricsHandler()
}
