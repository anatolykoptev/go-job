package applications

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// RED metrics for the applications pipeline.
// Namespace: gojob_, subsystem: application_.

var appRenderTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "gojob_application_render_total",
	Help: "Total application PDF render operations by kind (resume|cover) and outcome.",
}, []string{"kind", "outcome"}) // outcome: ok | skipped_no_binary | error

var appRenderDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Name:    "gojob_application_render_duration_seconds",
	Help:    "Elapsed time for a successful application PDF render (pandoc+typst+optimize).",
	Buckets: []float64{0.1, 0.25, 0.5, 1, 2, 5, 10, 15, 30},
}, []string{"kind"})

var appPersistTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "gojob_application_persist_total",
	Help: "Total Persist calls by outcome: ok_with_pdf | ok_md_only | error_md | error_pdf_write.",
}, []string{"outcome"})
// Note: the one-shot cmd/migrate-application-pdfs uses slog summary counters
// (ok/skipped/unmatched/errors) as its observable surface — no scrape target.
