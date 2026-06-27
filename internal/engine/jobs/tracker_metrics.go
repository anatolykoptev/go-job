package jobs

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var trackerOpsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "gojob_job_tracker_ops_total",
	Help: "Total number of job_tracker MCP tool operations.",
}, []string{"action", "store"})
