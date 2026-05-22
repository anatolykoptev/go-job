package engine

import (
	"fmt"
	"strings"
)

// Metric name constants.
//
// All counters carry the `_total` suffix to comply with Prometheus naming
// conventions; the go-kit/metrics Prometheus bridge exposes them under the
// `gojob_` namespace (e.g. `gojob_search_requests_total`).
const (
	MetricSearchRequests          = "search_requests_total"
	MetricLLMCalls                = "llm_calls_total"
	MetricLLMErrors               = "llm_errors_total"
	MetricFetchRequests           = "fetch_requests_total"
	MetricFetchErrors             = "fetch_errors_total"
	MetricDirectDDGRequests       = "direct_ddg_requests_total"
	MetricDirectStartpageRequests = "direct_startpage_requests_total"
	MetricFreelancerAPIRequests   = "freelancer_api_requests_total"
	MetricRemoteOKRequests        = "remoteok_requests_total"
	MetricWWRRequests             = "wwr_requests_total"
	MetricGitingestRequests       = "gitingest_requests_total"
	MetricYouTubeSearchRequests   = "youtube_search_requests_total"
	MetricYouTubeTranscriptReqs   = "youtube_transcript_requests_total"
	MetricHNJobsRequests          = "hn_jobs_requests_total"
	MetricGreenhouseRequests      = "greenhouse_requests_total"
	MetricLeverRequests           = "lever_requests_total"
	MetricYCJobsRequests          = "yc_jobs_requests_total"
	MetricIndeedRequests          = "indeed_requests_total"
	MetricHabrRequests            = "habr_requests_total"
	MetricCraigslistRequests      = "craigslist_requests_total"
	MetricAlgoraRequests          = "algora_requests_total"
	MetricSherlockRequests        = "sherlock_requests_total"
	MetricCantinaRequests         = "cantina_requests_total"
	MetricCode4renaRequests       = "code4rena_requests_total"
	MetricToolCalls               = "tool_calls_total"

	// MetricOversizeSpill is the base name for the labelled counter
	// gojob_oversize_spill_total{tool=<name>}.
	MetricOversizeSpill = "oversize_spill_total"

	// MetricOversizeBytes is the histogram of spill payload sizes in bytes
	// (gojob_oversize_bytes). Exposes P50/P95/P99 for capacity planning.
	// Bucket boundaries are configured in OversizeBytesBuckets (1KB–4MB log-scale).
	MetricOversizeBytes = "oversize_bytes"

	// MetricHuntIngest is the labelled counter gojob_hunt_ingest_total{kind,outcome}.
	// Incremented once per Upsert call in each search-tool ingest path.
	MetricHuntIngest = "hunt_ingest_total"
)

// OversizeBytesBuckets are log-scale bucket boundaries for spill payload sizes.
// Range 1KB–4MB covers typical MCP response overflow; each step is ~4×.
// Registered via reg.RegisterHistogram in engine.Init() before first Observe.
var OversizeBytesBuckets = []float64{1024, 4096, 16384, 65536, 262144, 1048576, 4194304}

// GetMetrics returns a snapshot of all metrics including cache stats.
func GetMetrics() map[string]int64 {
	m := reg.Snapshot()
	hits, misses := CacheStats()
	m["cache_hits_total"] = hits
	m["cache_misses_total"] = misses
	return m
}

// FormatMetrics returns metrics as a simple text format for HTTP endpoint.
func FormatMetrics() string {
	m := GetMetrics()
	keys := []string{
		MetricSearchRequests, MetricLLMCalls, MetricLLMErrors,
		MetricFetchRequests, MetricFetchErrors,
		MetricDirectDDGRequests, MetricDirectStartpageRequests,
		MetricFreelancerAPIRequests,
		MetricRemoteOKRequests, MetricWWRRequests,
		MetricGitingestRequests,
		MetricYouTubeSearchRequests, MetricYouTubeTranscriptReqs,
		MetricHNJobsRequests, MetricGreenhouseRequests, MetricLeverRequests, MetricYCJobsRequests,
		MetricIndeedRequests, MetricHabrRequests, MetricCraigslistRequests, MetricAlgoraRequests,
		MetricSherlockRequests, MetricCantinaRequests, MetricCode4renaRequests,
		MetricToolCalls,
		"cache_hits_total", "cache_misses_total",
	}
	var sb strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&sb, "%s %d\n", k, m[k])
	}
	return sb.String()
}

// Job-domain metric incrementors for sub-packages.

func IncrGitingestRequests()     { reg.Incr(MetricGitingestRequests) }
func IncrHNJobsRequests()        { reg.Incr(MetricHNJobsRequests) }
func IncrGreenhouseRequests()    { reg.Incr(MetricGreenhouseRequests) }
func IncrLeverRequests()         { reg.Incr(MetricLeverRequests) }
func IncrYCJobsRequests()        { reg.Incr(MetricYCJobsRequests) }
func IncrRemoteOKRequests()      { reg.Incr(MetricRemoteOKRequests) }
func IncrWWRRequests()           { reg.Incr(MetricWWRRequests) }
func IncrIndeedRequests()        { reg.Incr(MetricIndeedRequests) }
func IncrHabrRequests()          { reg.Incr(MetricHabrRequests) }
func IncrCraigslistRequests()    { reg.Incr(MetricCraigslistRequests) }
func IncrFreelancerAPIRequests() { reg.Incr(MetricFreelancerAPIRequests) }
func IncrAlgoraRequests()        { reg.Incr(MetricAlgoraRequests) }
func IncrSherlockRequests()      { reg.Incr(MetricSherlockRequests) }
func IncrCantinaRequests()       { reg.Incr(MetricCantinaRequests) }
func IncrCode4renaRequests()     { reg.Incr(MetricCode4renaRequests) }
func IncrYouTubeSearch()         { reg.Incr(MetricYouTubeSearchRequests) }
func IncrYouTubeTranscript()     { reg.Incr(MetricYouTubeTranscriptReqs) }
func IncrToolCall() { reg.Incr(MetricToolCalls) }

// IncrHuntIngest bumps gojob_hunt_ingest_total{kind=<kind>,outcome=<outcome>}.
// Called once per Upsert in the search-tool ingest path.
// Bounded label values: kind ∈ {bounty,job,freelance,security,audit_contest},
// outcome ∈ {created,merged,skipped}.
func IncrHuntIngest(kind, outcome string) {
	reg.Incr(MetricHuntIngest + "{kind=" + kind + ",outcome=" + outcome + "}")
}

// IncrOversizeSpill bumps gojob_oversize_spill_total{tool=<toolName>}.
// The go-kit/metrics prom bridge resolves the labelled name syntax
// "oversize_spill_total{tool=security_bounty_search}" into a CounterVec.
func IncrOversizeSpill(toolName string) {
	reg.Incr(MetricOversizeSpill + "{tool=" + toolName + "}")
}

// ObserveOversizeBytes records n bytes into the gojob_oversize_bytes histogram.
// Called once per spill with the payload size in bytes.
// Buckets are pre-configured via reg.RegisterHistogram in engine.Init().
func ObserveOversizeBytes(n int) {
	reg.Observe(MetricOversizeBytes, float64(n))
}
