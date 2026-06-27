package jobs

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// resumeMemoryOpsTotal counts resume_memory MCP tool operations by op and backend.
// backend label: "vector" (pgvector path) or "fts" (tsvector fallback).
var resumeMemoryOpsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "gojob_resume_memory_ops_total",
	Help: "Total number of resume_memory MCP tool operations.",
}, []string{"op", "backend"})

// resumeEmbedFailuresTotal counts embed errors that caused FTS fallback during add/update.
var resumeEmbedFailuresTotal = promauto.NewCounter(prometheus.CounterOpts{
	Name: "gojob_resume_embed_failures_total",
	Help: "Total embed failures that caused FTS-only storage in resume_vectors.",
})

