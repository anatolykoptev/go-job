package engine

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	kitmetrics "github.com/anatolykoptev/go-kit/metrics"
)

// Metric name constants.
const (
	MetricSearchRequests          = "search_requests"
	MetricLLMCalls                = "llm_calls"
	MetricLLMErrors               = "llm_errors"
	MetricFetchRequests           = "fetch_requests"
	MetricFetchErrors             = "fetch_errors"
	MetricDirectDDGRequests       = "direct_ddg_requests"
	MetricDirectStartpageRequests = "direct_startpage_requests"
	MetricFreelancerAPIRequests   = "freelancer_api_requests"
	MetricRemoteOKRequests        = "remoteok_requests"
	MetricWWRRequests             = "wwr_requests"
	MetricGitingestRequests       = "gitingest_requests"
	MetricYouTubeSearchRequests   = "youtube_search_requests"
	MetricYouTubeTranscriptReqs   = "youtube_transcript_requests"
	MetricHNJobsRequests          = "hn_jobs_requests"
	MetricGreenhouseRequests      = "greenhouse_requests"
	MetricLeverRequests           = "lever_requests"
	MetricYCJobsRequests          = "yc_jobs_requests"
	MetricIndeedRequests          = "indeed_requests"
	MetricHabrRequests            = "habr_requests"
	MetricCraigslistRequests      = "craigslist_requests"
)

var reg = kitmetrics.NewRegistry()

// GetMetrics returns a snapshot of all metrics including cache stats.
func GetMetrics() map[string]int64 {
	snap := reg.Snapshot()
	hits, misses := CacheStats()
	snap["cache_hits"] = hits
	snap["cache_misses"] = misses
	return snap
}

// FormatMetrics returns metrics as a simple text format for HTTP endpoint.
func FormatMetrics() string {
	m := GetMetrics()
	var sb strings.Builder
	keys := []string{
		MetricSearchRequests, MetricLLMCalls, MetricLLMErrors,
		MetricFetchRequests, MetricFetchErrors,
		MetricDirectDDGRequests, MetricDirectStartpageRequests,
		MetricFreelancerAPIRequests,
		MetricRemoteOKRequests, MetricWWRRequests,
		MetricGitingestRequests,
		MetricYouTubeSearchRequests, MetricYouTubeTranscriptReqs,
		MetricHNJobsRequests, MetricGreenhouseRequests, MetricLeverRequests, MetricYCJobsRequests,
		MetricIndeedRequests, MetricHabrRequests, MetricCraigslistRequests,
		"cache_hits", "cache_misses",
	}
	for _, k := range keys {
		fmt.Fprintf(&sb, "%s %d\n", k, m[k])
	}
	return sb.String()
}

// IncrGitingestRequests increments the gitingest request counter.
func IncrGitingestRequests()      { reg.Incr(MetricGitingestRequests) }
func IncrHNJobsRequests()         { reg.Incr(MetricHNJobsRequests) }
func IncrGreenhouseRequests()     { reg.Incr(MetricGreenhouseRequests) }
func IncrLeverRequests()          { reg.Incr(MetricLeverRequests) }
func IncrYCJobsRequests()         { reg.Incr(MetricYCJobsRequests) }
func IncrRemoteOKRequests()       { reg.Incr(MetricRemoteOKRequests) }
func IncrWWRRequests()            { reg.Incr(MetricWWRRequests) }
func IncrIndeedRequests()         { reg.Incr(MetricIndeedRequests) }
func IncrHabrRequests()           { reg.Incr(MetricHabrRequests) }
func IncrCraigslistRequests()     { reg.Incr(MetricCraigslistRequests) }
func IncrFreelancerAPIRequests()  { reg.Incr(MetricFreelancerAPIRequests) }
func IncrYouTubeSearch()          { reg.Incr(MetricYouTubeSearchRequests) }
func IncrYouTubeTranscript()      { reg.Incr(MetricYouTubeTranscriptReqs) }

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
