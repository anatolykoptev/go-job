package engine

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"
)

// Metrics tracks operational counters across the engine.
var metrics struct {
	SearchRequests          atomic.Int64
	LLMCalls                atomic.Int64
	LLMErrors               atomic.Int64
	FetchRequests           atomic.Int64
	FetchErrors             atomic.Int64
	DirectDDGRequests       atomic.Int64
	DirectStartpageRequests atomic.Int64
	FreelancerAPIRequests   atomic.Int64
	RemoteOKRequests        atomic.Int64
	WWRRequests             atomic.Int64
	GitingestRequests          atomic.Int64
	YouTubeSearchRequests      atomic.Int64
	YouTubeTranscriptRequests  atomic.Int64
	HNJobsRequests             atomic.Int64
	GreenhouseRequests         atomic.Int64
	LeverRequests              atomic.Int64
	YCJobsRequests             atomic.Int64
}

// GetMetrics returns a snapshot of all metrics including cache stats.
func GetMetrics() map[string]int64 {
	hits, misses := CacheStats()
	return map[string]int64{
		"search_requests":           metrics.SearchRequests.Load(),
		"llm_calls":                 metrics.LLMCalls.Load(),
		"llm_errors":                metrics.LLMErrors.Load(),
		"fetch_requests":            metrics.FetchRequests.Load(),
		"fetch_errors":              metrics.FetchErrors.Load(),
		"direct_ddg_requests":       metrics.DirectDDGRequests.Load(),
		"direct_startpage_requests": metrics.DirectStartpageRequests.Load(),
		"freelancer_api_requests":   metrics.FreelancerAPIRequests.Load(),
		"remoteok_requests":         metrics.RemoteOKRequests.Load(),
		"wwr_requests":              metrics.WWRRequests.Load(),
		"gitingest_requests":           metrics.GitingestRequests.Load(),
		"youtube_search_requests":      metrics.YouTubeSearchRequests.Load(),
		"youtube_transcript_requests":  metrics.YouTubeTranscriptRequests.Load(),
		"hn_jobs_requests":             metrics.HNJobsRequests.Load(),
		"greenhouse_requests":          metrics.GreenhouseRequests.Load(),
		"lever_requests":               metrics.LeverRequests.Load(),
		"yc_jobs_requests":             metrics.YCJobsRequests.Load(),
		"cache_hits":                  hits,
		"cache_misses":                misses,
	}
}

// FormatMetrics returns metrics as a simple text format for HTTP endpoint.
func FormatMetrics() string {
	m := GetMetrics()
	var sb strings.Builder
	keys := []string{
		"search_requests", "llm_calls", "llm_errors",
		"fetch_requests", "fetch_errors",
		"direct_ddg_requests", "direct_startpage_requests",
		"freelancer_api_requests",
		"remoteok_requests", "wwr_requests",
		"gitingest_requests",
		"youtube_search_requests", "youtube_transcript_requests",
		"hn_jobs_requests", "greenhouse_requests", "lever_requests", "yc_jobs_requests",
		"cache_hits", "cache_misses",
	}
	for _, k := range keys {
		fmt.Fprintf(&sb, "%s %d\n", k, m[k])
	}
	return sb.String()
}

// IncrGitingestRequests increments the gitingest request counter.
func IncrGitingestRequests() {
	metrics.GitingestRequests.Add(1)
}

// Incrementors for jobs/ sub-package.
func IncrHNJobsRequests()    { metrics.HNJobsRequests.Add(1) }
func IncrGreenhouseRequests() { metrics.GreenhouseRequests.Add(1) }
func IncrLeverRequests()     { metrics.LeverRequests.Add(1) }
func IncrYCJobsRequests()    { metrics.YCJobsRequests.Add(1) }
func IncrRemoteOKRequests()  { metrics.RemoteOKRequests.Add(1) }
func IncrWWRRequests()       { metrics.WWRRequests.Add(1) }

// Incrementors for sources/ sub-package.
func IncrFreelancerAPIRequests()  { metrics.FreelancerAPIRequests.Add(1) }
func IncrYouTubeSearch()          { metrics.YouTubeSearchRequests.Add(1) }
func IncrYouTubeTranscript()      { metrics.YouTubeTranscriptRequests.Add(1) }

// TrackOperation logs a warning if an operation takes longer than threshold.
func TrackOperation(ctx context.Context, name string, fn func(context.Context) error) error {
	start := time.Now()
	err := fn(ctx)
	elapsed := time.Since(start)
	if elapsed > 5*time.Second {
		slog.Warn("slow operation", slog.String("op", name), slog.Duration("elapsed", elapsed))
	}
	return err
}
