package engine

import (
	"context"
	"errors"
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
	MetricAshbyRequests           = "ashby_requests_total"
	MetricYCJobsRequests          = "yc_jobs_requests_total"
	MetricIndeedRequests          = "indeed_requests_total"
	MetricHabrRequests            = "habr_requests_total"
	MetricCraigslistRequests      = "craigslist_requests_total"
	MetricAlgoraRequests          = "algora_requests_total"
	MetricAlgoraJobsRequests      = "algora_jobs_requests_total"
	MetricSherlockRequests        = "sherlock_requests_total"
	MetricCantinaRequests         = "cantina_requests_total"
	MetricCode4renaRequests       = "code4rena_requests_total"
	MetricToolCalls               = "tool_calls_total"

	// Shared bounded-label values reused across metric incrementors and the flat
	// text endpoint (extracted to satisfy goconst min-occurrences=4).
	outcomeOK        = "ok"
	outcomeEmpty     = "empty"
	outcomeTimeout   = "timeout"
	outcomeError     = "error"
	outcomeNoKey     = "no_key"
	outcomeParseFail = "parse_fail"
	kindJobs         = "jobs"
	kindBounties     = "bounties"
	kindFreelance    = "freelance"
	kindSecurity     = "security"

	// MetricLLMModelsDropped counts model ids absent from /v1/models at chain
	// construction time (gojob_llm_models_dropped_total).
	// Bumped once per dropped model id; a non-zero value means the env chain has
	// a dead entry that should be removed from LLM_MODEL_FALLBACK.
	MetricLLMModelsDropped = "llm_models_dropped_total"

	// MetricLLMChainDegraded counts chain construction calls where the health
	// filter was skipped (gojob_llm_chain_degraded_total). reason label ∈
	// {"no_registry","fetch_failed","empty_set","all_filtered"}.
	MetricLLMChainDegraded = "llm_chain_degraded_total"

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

	// MetricHuntNotify is the labelled counter gojob_hunt_notify_total{outcome}.
	// Incremented by the Telegram notifier after each send attempt.
	// outcome ∈ {"sent", "failed"}.
	MetricHuntNotify = "hunt_notify_total"

	// MetricCompanyResearch is the labelled counter
	// gojob_company_research_total{outcome}. Bumped once per bounded
	// company-research attempt on an optional enrichment path (resume_generate,
	// application_prep). outcome ∈ {"ok","timeout","error"}.
	//
	// outcome=timeout is the signal that previously went SILENT: a slow SearXNG
	// + LLM company-research substep would push the whole tool past the 90s
	// ToolTimeout with no dedicated metric. A rising timeout rate means the
	// research substep is degrading and the resume/application is shipping
	// without company context — visible now instead of surfacing only as a
	// tool-level timeout.
	MetricCompanyResearch = "company_research_total"

	// MetricPlatformResults is the labelled counter
	// gojob_platform_results_total{platform,outcome}. Bumped ONCE per job_search
	// call in the collector fan-in loop (tool_job_search.go), covering all 18
	// platforms uniformly after each connector goroutine returns.
	// platform ∈ validPlatforms, outcome ∈ {"results","empty","error"} — bounded.
	//
	// This is the signal that was MISSING when every direct-scraper platform went
	// silently dead: the per-platform *_requests_total counters bump at connector
	// ENTRY, so they kept incrementing while results stayed zero — "reached but
	// produced nothing" was indistinguishable from "reached and produced jobs".
	// A platform whose discovery dependency dies now shows outcome=empty rising
	// while outcome=results stays flat, instead of surfacing only weeks later as
	// "search returns null". Alert target: ratio of empty/(results+empty) per
	// platform trending to 1.0.
	MetricPlatformResults = "platform_results_total"

	// MetricSourceDuration is the histogram gojob_source_duration_seconds{platform}.
	// Observed once per connector return in runSource, measuring wall time from
	// Fetch start to result send. Provides the Duration (D) leg of the RED method
	// per source — the operator named "duration" as missing diagnostics (ask #4).
	// Buckets: 0.1s–120s (covers fast JSON APIs to slow UN scraper fan-outs).
	MetricSourceDuration = "source_duration_seconds"

	// MetricHuntList is the labelled counter gojob_hunt_list_total{kind}.
	// Bumped once per hunt_list call that the SERVER actually handled, after
	// the rows are fetched. kind ∈ {jobs,bounties,freelance,security}.
	//
	// Diagnostic value: a client-reported "socket connection closed
	// unexpectedly" on hunt_list is, per investigation, a client-side idle
	// keep-alive reuse race — the failing request never reaches the server. If
	// that recurs, this counter NOT incrementing for the failed attempt (while
	// the retry does increment it) is the positive proof the close was
	// client-side, not a server-side mid-response drop. It turns a previously
	// un-attributable transport error into an answerable question.
	MetricHuntList = "hunt_list_total"

	// MetricHuntDiscoveryURLs is the labelled counter
	// gojob_hunt_discovery_urls_total{platform}.
	// Bumped once per discovery call with the number of board URLs found.
	// platform ∈ {greenhouse,lever,ashby}. A sustained zero signals the
	// slug-discovery substep is empty (the regression that caused the 2026-06-22
	// collapse) before the hunt_jobs table goes stale.
	MetricHuntDiscoveryURLs = "hunt_discovery_urls_total"

	// MetricHuntDiscoverySource is the labelled counter
	// gojob_hunt_discovery_source_total{source}.
	// source ∈ {"go-search","local-fallback"} — the Scenario-2 discriminator:
	// when go-search is unreachable the counter shifts to local-fallback so ops
	// can see "running on degraded DDG floor" without waiting for the table to go stale.
	MetricHuntDiscoverySource = "hunt_discovery_source_total"

	// MetricHuntCycleDuration is the histogram of ingest worker cycle durations
	// gojob_hunt_cycle_duration_seconds.  Buckets cover 1s–10m.
	MetricHuntCycleDuration = "hunt_cycle_duration_seconds"
)

// OversizeBytesBuckets are log-scale bucket boundaries for spill payload sizes.
// Range 1KB–4MB covers typical MCP response overflow; each step is ~4×.
// Registered via reg.RegisterHistogram in engine.Init() before first Observe.
var OversizeBytesBuckets = []float64{1024, 4096, 16384, 65536, 262144, 1048576, 4194304}

// SourceDurationBuckets covers per-connector Fetch latency (seconds).
// Range 0.1s–120s: fast JSON APIs (remoteok, habr ~0.2s) through slow UN portals
// (inspira, undp ~60–90s fan-out). Steps: 0.1, 0.5, 1, 2, 5, 10, 30, 60, 120.
var SourceDurationBuckets = []float64{0.1, 0.5, 1, 2, 5, 10, 30, 60, 120}

// HuntCycleDurationBuckets covers one ATS ingest worker cycle (seconds).
// A cycle spans N queries × 3 platforms, each capped at 45s:
//   worst-case = 3 queries × 3 platforms × 45s = 405s ≈ 7m.
// Buckets: 1s, 5s, 15s, 30s, 1m, 2m, 5m, 10m — covers the full range.
// Registered via reg.RegisterHistogram in engine.Init() before first Observe.
var HuntCycleDurationBuckets = []float64{1, 5, 15, 30, 60, 120, 300, 600}

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
		MetricHNJobsRequests, MetricGreenhouseRequests, MetricLeverRequests, MetricAshbyRequests, MetricYCJobsRequests,
		MetricIndeedRequests, MetricHabrRequests, MetricCraigslistRequests, MetricAlgoraRequests, MetricAlgoraJobsRequests,
		MetricSherlockRequests, MetricCantinaRequests, MetricCode4renaRequests,
		MetricToolCalls,
		"cache_hits_total", "cache_misses_total",
	}
	// Labelled counters (company_research_total{outcome}) are emitted by the
	// go-kit prom bridge directly; the flat text endpoint surfaces the bounded
	// outcomes explicitly so a rising timeout rate is visible here too.
	for _, oc := range []string{outcomeOK, outcomeTimeout, outcomeError} {
		keys = append(keys, MetricCompanyResearch+"{outcome="+oc+"}")
	}
	// Per-kind server-handled hunt_list counters, surfaced on the flat endpoint
	// so a "no rows / socket drop" report can be checked against whether the
	// server actually handled the call.
	for _, k := range []string{kindJobs, kindBounties, kindFreelance, kindSecurity} {
		keys = append(keys, MetricHuntList+"{kind="+k+"}")
	}
	// Per-platform outcome counters pre-touched here so rate()-floor alerts see
	// 0 (not no-data) before the first job_search call. Labels are bounded enums
	// (≤18 platforms × 6 outcomes = ≤108 series) — cardinality is safe.
	// ADR-J3: outcome set extended from {results,empty,error} to
	// {ok,empty,error,timeout,no_key,parse_fail}. "results" renamed to "ok".
	// Mirrors the company_research / hunt_list treatment above.
	// allPlatforms is also used by the duration loop below (shared to avoid goconst).
	allPlatforms := []string{
		"linkedin", DiscoveryPlatformGreenhouse, DiscoveryPlatformLever, DiscoveryPlatformAshby,
		"yc", "hn", "indeed",
		"habr", "twitter", "craigslist", "remoteok", "weworkremotely",
		"remotive", "freelancer", "google", "inspira", "undp", "searxng",
	}
	for _, p := range allPlatforms {
		for _, oc := range []string{outcomeOK, outcomeEmpty, outcomeError, outcomeTimeout, outcomeNoKey, outcomeParseFail} {
			keys = append(keys, MetricPlatformResults+"{platform="+p+",outcome="+oc+"}")
		}
	}
	// Per-platform source duration: pre-touch one histogram series per platform
	// (searxng is a meta-source without a real Fetch, so omitted here).
	for _, p := range allPlatforms[:len(allPlatforms)-1] { // exclude trailing "searxng"
		keys = append(keys, MetricSourceDuration+"{platform="+p+"}")
	}
	// Discovery source counters pre-touched so rate()-floor alerts see 0 when
	// HUNT_INGEST_ENABLED is off (or go-search is healthy but idle) — same class
	// as per-platform outcome above.  Both validDiscoverySources and
	// validDiscoveryPlatforms are bounded enums (2 × 3 = 6 series).
	for _, src := range []string{"go-search", "local-fallback"} {
		keys = append(keys, MetricHuntDiscoverySource+"{source="+src+"}")
	}
	for _, p := range []string{DiscoveryPlatformGreenhouse, DiscoveryPlatformLever, DiscoveryPlatformAshby} {
		keys = append(keys, MetricHuntDiscoveryURLs+"{platform="+p+"}")
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
func IncrAshbyRequests()         { reg.Incr(MetricAshbyRequests) }
func IncrYCJobsRequests()        { reg.Incr(MetricYCJobsRequests) }
func IncrRemoteOKRequests()      { reg.Incr(MetricRemoteOKRequests) }
func IncrWWRRequests()           { reg.Incr(MetricWWRRequests) }
func IncrIndeedRequests()        { reg.Incr(MetricIndeedRequests) }
func IncrHabrRequests()          { reg.Incr(MetricHabrRequests) }
func IncrCraigslistRequests()    { reg.Incr(MetricCraigslistRequests) }
func IncrFreelancerAPIRequests() { reg.Incr(MetricFreelancerAPIRequests) }
func IncrAlgoraRequests()        { reg.Incr(MetricAlgoraRequests) }
func IncrAlgoraJobsRequests()    { reg.Incr(MetricAlgoraJobsRequests) }
func IncrSherlockRequests()      { reg.Incr(MetricSherlockRequests) }
func IncrCantinaRequests()       { reg.Incr(MetricCantinaRequests) }
func IncrCode4renaRequests()     { reg.Incr(MetricCode4renaRequests) }
func IncrYouTubeSearch()         { reg.Incr(MetricYouTubeSearchRequests) }
func IncrYouTubeTranscript()     { reg.Incr(MetricYouTubeTranscriptReqs) }
func IncrToolCall() { reg.Incr(MetricToolCalls) }

// validHuntKinds is the allowlist for the hunt_ingest_total `kind` label.
// Unknown kinds are rejected to prevent Prometheus cardinality explosion.
var validHuntKinds = map[string]bool{
	"bounty":        true,
	"job":           true,
	kindFreelance:   true,
	kindSecurity:    true,
	"audit_contest": true,
}

// IncrHuntIngest bumps gojob_hunt_ingest_total{kind=<kind>,outcome=<outcome>}.
// Called once per Upsert in the search-tool ingest path.
// Bounded label values: kind ∈ {bounty,job,freelance,security,audit_contest},
// outcome ∈ {created,merged,skipped,error}.
// Unrecognised kind values are silently dropped to prevent label cardinality blowup.
func IncrHuntIngest(kind, outcome string) {
	if !validHuntKinds[kind] {
		return
	}
	reg.Incr(MetricHuntIngest + "{kind=" + kind + ",outcome=" + outcome + "}")
}

// validHuntListKinds is the allowlist for the hunt_list_total `kind` label.
// Note these are the PLURAL list-tool kinds (jobs/bounties/...), distinct from
// the singular ingest kinds in validHuntKinds (job/bounty/...).
var validHuntListKinds = map[string]bool{
	kindJobs:      true,
	kindBounties:  true,
	kindFreelance: true,
	kindSecurity:  true,
}

// IncrHuntList bumps gojob_hunt_list_total{kind=<kind>}, once per server-handled
// hunt_list call. kind ∈ {jobs,bounties,freelance,security} — bounded label.
// Unrecognised kinds are silently dropped (cardinality guard).
func IncrHuntList(kind string) {
	if !validHuntListKinds[kind] {
		return
	}
	reg.Incr(MetricHuntList + "{kind=" + kind + "}")
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

// IncrHuntNotify bumps gojob_hunt_notify_total{outcome=<outcome>}.
// outcome must be "sent" or "failed" — bounded label (no cardinality risk).
// Called by the Telegram notifier after each send attempt.
func IncrHuntNotify(outcome string) {
	reg.Incr(MetricHuntNotify + "{outcome=" + outcome + "}")
}

// validPlatforms is the allowlist for the platform_results_total `platform`
// label. Mirrors the advertised job_search platform enum; unknown values are
// dropped to bound cardinality.
var validPlatforms = map[string]bool{
	"linkedin": true, "greenhouse": true, "lever": true, "ashby": true,
	"yc": true, "hn": true, "indeed": true, "habr": true, "twitter": true,
	"craigslist": true, "remoteok": true, "weworkremotely": true,
	"remotive": true, "freelancer": true, "google": true,
	"inspira": true, "undp": true, "searxng": true,
}

// validPlatformOutcomes bounds the outcome label for platform_results_total.
//
// ADR-J3 (P3): vocabulary enriched from {results,empty,error} to
// {ok,empty,error,timeout,no_key,parse_fail}. The "results" value is RENAMED to
// "ok" for alignment with the company_research and discovery counters (both use
// "ok"). Any existing dashboard/alert querying outcome="results" must update to
// outcome="ok" — grep Prometheus rules and Grafana for `outcome=` before cutover.
// At go-job P3 no external dashboards key off this label, so direct cut-over is safe.
var validPlatformOutcomes = map[string]bool{
	outcomeOK: true, outcomeEmpty: true, outcomeError: true,
	outcomeTimeout: true, outcomeNoKey: true, outcomeParseFail: true,
}

// IncrPlatformResults bumps gojob_platform_results_total{platform=<p>,outcome=<o>}.
// platform ∈ validPlatforms, outcome ∈ {"results","empty","error"} — both bounded.
// Called ONCE per connector return in the job_search collector fan-in loop,
// covering all 18 platforms uniformly. Per-connector call sites were removed to
// avoid double-counting. A silently-dead connector is now observable as
// outcome=empty rising while outcome=results stays flat.
func IncrPlatformResults(platform, outcome string) {
	if !validPlatforms[platform] || !validPlatformOutcomes[outcome] {
		return
	}
	reg.Incr(MetricPlatformResults + "{platform=" + platform + ",outcome=" + outcome + "}")
}

// PlatformOutcome classifies a connector return (n results, err) into the bounded
// outcome label for IncrPlatformResults. err takes precedence over emptiness.
//
// ADR-J3 (P3): vocabulary {ok,empty,error,timeout,no_key,parse_fail}.
//   - context.DeadlineExceeded / context.Canceled → "timeout"
//   - errors.Is(err, jobs.ErrNoAPIKey)            → "no_key"
//   - errors.Is(err, jobs.ErrParse)               → "parse_fail"
//   - any other non-nil error                      → "error"
//   - n > 0, err == nil                            → "ok"  (renamed from "results")
//   - n == 0, err == nil                           → "empty"
//
// The engine package cannot import internal/engine/jobs (cycle) so the sentinel
// errors are passed as interface values; callers that need to classify must import
// jobs and pass the err directly — the errors.Is chain traverses wraps correctly.
func PlatformOutcome(n int, err error) string {
	if err != nil {
		// Check specific classes before falling back to generic error.
		// errors.Is traverses the error chain, so fmt.Errorf("...: %w", ErrNoAPIKey)
		// is correctly matched here.
		if isDeadlineErr(err) {
			return outcomeTimeout
		}
		if isNoAPIKeyErr(err) {
			return outcomeNoKey
		}
		if isParseErr(err) {
			return outcomeParseFail
		}
		return outcomeError
	}
	if n > 0 {
		return outcomeOK
	}
	return outcomeEmpty
}

// isDeadlineErr reports whether err is a context deadline/cancellation error.
// Extracted for testability.
func isDeadlineErr(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

// isNoAPIKeyErr and isParseErr are resolved via interface-based sentinel matching.
// The engine package imports internal/engine/jobs indirectly through the connector
// path, so we avoid a direct import of jobs here by using a package-level hook
// populated at init time (see metrics_hooks.go).
//
// Until a hook is registered, the check is a no-op (returns false), meaning
// the error degrades to outcomeError — correct pre-P3 behaviour.
var isNoAPIKeyErr func(error) bool = func(error) bool { return false }
var isParseErr func(error) bool = func(error) bool { return false }

// validCompanyResearchOutcomes bounds the outcome label to prevent cardinality
// blowup from arbitrary error strings.
var validCompanyResearchOutcomes = map[string]bool{
	outcomeOK: true, outcomeTimeout: true, outcomeError: true,
}

// IncrCompanyResearch bumps gojob_company_research_total{outcome=<outcome>}.
// outcome ∈ {"ok","timeout","error"} — bounded label. Unrecognised values are
// silently dropped. Called once per bounded company-research attempt.
func IncrCompanyResearch(outcome string) {
	if !validCompanyResearchOutcomes[outcome] {
		return
	}
	reg.Incr(MetricCompanyResearch + "{outcome=" + outcome + "}")
}

// ATS platform label constants for the hunt_discovery_* metrics.
// Defined here (not in engine/jobs) so metrics.go can use them in FormatMetrics
// pre-touch loops without creating a cross-package import cycle.
const (
	DiscoveryPlatformGreenhouse = "greenhouse"
	DiscoveryPlatformLever      = "lever"
	DiscoveryPlatformAshby      = "ashby"
)

// validDiscoveryPlatforms bounds the platform label for hunt_discovery_urls_total.
var validDiscoveryPlatforms = map[string]bool{
	DiscoveryPlatformGreenhouse: true,
	DiscoveryPlatformLever:      true,
	DiscoveryPlatformAshby:      true,
}

// validDiscoverySources bounds the source label for hunt_discovery_source_total.
// "go-search" = fused multi-source path (Brave-API + ox-browser + DDG via go-search).
// "local-fallback" = degraded DDG/Marginalia-only path (go-job's own SearchDirect).
var validDiscoverySources = map[string]bool{
	"go-search": true, "local-fallback": true,
}

// IncrHuntDiscoveryURLs adds n to gojob_hunt_discovery_urls_total{platform=<p>}.
// n is the number of board URLs returned by the discovery step for this platform.
// n=0 is accepted and treated as Add(0) which initializes the series to 0 —
// makes "zero URLs found" visible as a flat counter rather than a missing series,
// guarding the 2026-06-22 silent-collapse class.
func IncrHuntDiscoveryURLs(platform string, n int) {
	if !validDiscoveryPlatforms[platform] {
		return
	}
	reg.Add(MetricHuntDiscoveryURLs+"{platform="+platform+"}", int64(n))
}

// IncrHuntDiscoverySource bumps gojob_hunt_discovery_source_total{source=<s>}.
// source ∈ {"go-search","local-fallback"}. Unrecognised values are dropped.
func IncrHuntDiscoverySource(source string) {
	if !validDiscoverySources[source] {
		return
	}
	reg.Incr(MetricHuntDiscoverySource + "{source=" + source + "}")
}

// ObserveHuntCycleDuration records a worker cycle duration into
// gojob_hunt_cycle_duration_seconds.
func ObserveHuntCycleDuration(d float64) {
	reg.Observe(MetricHuntCycleDuration, d)
}

// ObserveSourceDuration records a connector's Fetch wall time into
// gojob_source_duration_seconds{platform=<p>}.
// Called once per runSource invocation after the connector returns.
// Unknown platform values are silently dropped (bounded-label guard).
func ObserveSourceDuration(platform string, seconds float64) {
	if !validPlatforms[platform] {
		return
	}
	reg.Observe(MetricSourceDuration+"{platform="+platform+"}", seconds)
}
