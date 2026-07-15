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

	// Fit-scoring filter stage labels (hunt_score_filtered_total{stage}).
	// Extracted to satisfy goconst (appear ≥3 times across allowlist + FormatMetrics).
	scoreFilterRecency = "recency"
	scoreFilterJaccard = "jaccard"

	// Fit-scoring LLM result labels (hunt_score_llm_total{result}).
	// Extracted to satisfy goconst (appear ≥3 times across allowlist + FormatMetrics).
	scoreLLMOk        = "ok"
	scoreLLMEnumClamp = "enum_clamp"
	scoreLLMParseFail = "parse_fail"
	scoreLLMError     = "llm_error"

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
	// Incremented by the Telegram notifier after each send attempt or recency-gate
	// decision. outcome ∈ {"sent", "failed", "stale", "no_date"}.
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

	// MetricATSFetchErrors is the labelled counter
	// gojob_ats_fetch_errors_total{platform,reason}.
	// Bumped at each ATS board-fetch error exit (lever/greenhouse/ashby).
	// platform ∈ {greenhouse,lever,ashby}, reason ∈ {parse,truncated,status,transport}.
	// The Debug-only error logging that existed before P5 was the root cause of the
	// insiderone silent-empty class: a 3.75 MB board was truncated at the old 2 MB cap,
	// json.Unmarshal failed "Unterminated string", the error was only logged at Debug
	// level, and 0 results propagated silently. This counter makes "discovered slug but
	// board-fetch failed" visible on the flat metrics endpoint and in Prometheus.
	MetricATSFetchErrors = "ats_fetch_errors_total"

	// MetricHuntDiscoveryVariants is the labelled counter
	// gojob_hunt_discovery_variants_total{platform,result}.
	// Bumped once per variant query in unionDiscoverSlugs.
	// platform ∈ {greenhouse,lever,ashby}, result ∈ {hit,miss}.
	// A sustained miss rate signals query templates need tuning.
	MetricHuntDiscoveryVariants = "hunt_discovery_variants_total"

	// MetricHuntPostedAt is the labelled counter
	// gojob_hunt_posted_at_total{platform,present}.
	// Bumped once per ATS row ingested by the worker (SearxngResultToHuntJob path).
	// platform ∈ {greenhouse,lever,ashby}, present ∈ {"true","false"}.
	// present="true" means the ATS API date (greenhouse updated_at / lever createdAt /
	// ashby publishedAt) parsed into hunt_jobs.posted_at; present="false" means the
	// row landed with a NULL posted_at and will be skipped by the #70 recency gate.
	// This is the regression discriminator for the "ATS rows lose the date" class:
	// a sustained present=false floor distinguishes "the date-threading fix regressed"
	// from "the ATS API stopped sending the field" (the latter shows in BOTH the
	// false count rising AND the upstream board response), before the recency gate
	// silently skips every ATS posting as no_date.
	MetricHuntPostedAt = "hunt_posted_at_total"

	// MetricSlugCacheSize is not exposed as a counter (it is a gauge).
	// Logged via slog; increment/decrement not tracked here.

	// MetricSlugCacheEvictions is the labelled counter
	// gojob_hunt_slug_cache_evictions_total{platform,reason}.
	// platform ∈ {greenhouse,lever,ashby}, reason ∈ {lru,board_404,ttl}.
	// lru=size-pressure; board_404=HTTP 404 from board-fetch; ttl=reserved.
	MetricSlugCacheEvictions = "hunt_slug_cache_evictions_total"

	// MetricHuntFitScore is the histogram of fit-score values for LLM-scored jobs
	// gojob_hunt_fit_score. Observed once per LLM call (LLMCalled=true).
	// Answers "what is my fit distribution?" so the operator can tune HUNT_NOTIFY_MIN_FIT.
	// Buckets: 0, 20, 40, 60, 80, 100 — evenly spaced over the 0-100 scale.
	// Registered via reg.RegisterHistogram in engine.Init() before first Observe.
	MetricHuntFitScore = "hunt_fit_score"

	// MetricHuntScoreFiltered is the labelled counter
	// gojob_hunt_score_filtered_total{stage}.
	// stage ∈ {"recency","jaccard"} — the two pre-LLM drop points.
	// recency: job was stale (posted_at nil or > HUNT_NOTIFY_MAX_AGE).
	// jaccard: job was below the Jaccard pre-filter threshold.
	// Pre-touch both in FormatMetrics so rate()-floor alerts see 0 before first run.
	MetricHuntScoreFiltered = "hunt_score_filtered_total"

	// MetricHuntScoreLLM is the labelled counter
	// gojob_hunt_score_llm_total{result}.
	// result ∈ {"ok","enum_clamp","parse_fail","llm_error"} — bounded LLM outcome.
	// ok: LLM returned valid parseable JSON with known enum values.
	// enum_clamp: JSON parsed but success_band or over_under was unknown and clamped.
	// parse_fail: JSON could not be parsed (fail-open → unscored).
	// llm_error: LLM call itself failed (fail-open → unscored).
	// Pre-touch all four in FormatMetrics so rate()-floor alerts see 0 before first LLM call.
	MetricHuntScoreLLM = "hunt_score_llm_total"

	// MetricHuntPersistEnabled is the startup gauge gojob_hunt_persist_enabled.
	// Set to 1 when hunt store is wired and migrations succeed; 0 otherwise.
	// No labels -- cardinality guard.
	MetricHuntPersistEnabled = "hunt_persist_enabled"

	// MetricVacancyIngest is the labelled counter gojob_vacancy_ingest_total{result}.
	// result bounded: ok, weak, skipped_store.
	MetricVacancyIngest = "vacancy_ingest_total"

	// MetricHuntScoreBreakerTrips is the counter gojob_hunt_score_breaker_trips_total.
	// Incremented when the LLM circuit breaker trips (llmCallsThisCycle >= maxLLM),
	// preventing LLM calls for the rest of the cycle. No labels — cardinality guard.
	// Pre-touched at zero in FormatMetrics so rate()-floor alerts see 0 before first trip.
	// Alert: rate(gojob_hunt_score_breaker_trips_total[5m]) > 0 → LLM budget exhausted.
	MetricHuntScoreBreakerTrips = "hunt_score_breaker_trips_total"

	// MetricHuntScorePersistFailures is the counter gojob_hunt_score_persist_failures_total.
	// Incremented when SetJobScore fails after all retry attempts are exhausted.
	// No labels — cardinality guard. Pre-touched at zero in FormatMetrics.
	// Alert: rate(gojob_hunt_score_persist_failures_total[5m]) > 0 → DB health issues.
	MetricHuntScorePersistFailures = "hunt_score_persist_failures_total"
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
//
//	worst-case = 3 queries × 3 platforms × 45s = 405s ≈ 7m.
//
// Buckets: 1s, 5s, 15s, 30s, 1m, 2m, 5m, 10m — covers the full range.
// Registered via reg.RegisterHistogram in engine.Init() before first Observe.
var HuntCycleDurationBuckets = []float64{1, 5, 15, 30, 60, 120, 300, 600}

// HuntFitScoreBuckets are evenly-spaced bucket boundaries for the fit-score histogram.
// Range 0–100 in 20-point steps: 0, 20, 40, 60, 80, 100.
// Registered via reg.RegisterHistogram in engine.Init() before first Observe.
var HuntFitScoreBuckets = []float64{0, 20, 40, 60, 80, 100}

// GetMetrics returns a snapshot of all metrics including cache stats.
// Returns an empty map if the registry has not been initialised (e.g. in
// package-external unit tests that do not call engine.Init).
func GetMetrics() map[string]int64 {
	if reg == nil {
		return map[string]int64{}
	}
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
	// as per-platform outcome above.  validDiscoverySources and
	// validDiscoveryPlatforms are bounded enums (3 × 3 = 9 series).
	for _, src := range []string{"go-search", "local-fallback", "degraded-fallback"} {
		keys = append(keys, MetricHuntDiscoverySource+"{source="+src+"}")
	}
	for _, p := range []string{DiscoveryPlatformGreenhouse, DiscoveryPlatformLever, DiscoveryPlatformAshby} {
		keys = append(keys, MetricHuntDiscoveryURLs+"{platform="+p+"}")
	}
	// ATS fetch-error counters pre-touched so rate()-floor alerts see 0 before the
	// first board-fetch error — same pattern as discovery URL pre-touch above.
	// 3 platforms × 4 reasons = 12 series (bounded, safe cardinality).
	for _, p := range []string{DiscoveryPlatformGreenhouse, DiscoveryPlatformLever, DiscoveryPlatformAshby} {
		for _, r := range []string{"parse", "truncated", "status", "transport"} {
			keys = append(keys, MetricATSFetchErrors+"{platform="+p+",reason="+r+"}")
		}
	}
	// hunt_notify_total pre-touched for all outcomes so rate()-floor alerts see 0
	// before the first notify fire.
	// outcome ∈ {"sent","failed","stale","no_date","low_fit","unscored"}.
	// "low_fit" — fit gate dropped the job (fit_score < HUNT_NOTIFY_MIN_FIT).
	// "unscored" — LLM scorer failed, notified with degraded card (fail-open).
	for _, oc := range []string{"sent", "failed", "stale", "no_date", "low_fit", "unscored"} {
		keys = append(keys, MetricHuntNotify+"{outcome="+oc+"}")
	}
	// Discovery variant counters (P1 multi-query union).
	// 3 platforms × 2 results = 6 series.
	for _, p := range []string{DiscoveryPlatformGreenhouse, DiscoveryPlatformLever, DiscoveryPlatformAshby} {
		for _, res := range []string{"hit", "miss"} {
			keys = append(keys, MetricHuntDiscoveryVariants+"{platform="+p+",result="+res+"}")
		}
	}
	// Slug cache eviction counters (P2 runtime slug cache).
	// 3 platforms × 3 reasons = 9 series (lru=size-pressure, board_404=404-evict, ttl=reserved).
	for _, p := range []string{DiscoveryPlatformGreenhouse, DiscoveryPlatformLever, DiscoveryPlatformAshby} {
		for _, r := range []string{"lru", "board_404", "ttl"} {
			keys = append(keys, MetricSlugCacheEvictions+"{platform="+p+",reason="+r+"}")
		}
	}
	// posted_at population counters pre-touched so a present=false floor is visible
	// before the first ingest cycle. 3 platforms × 2 present values = 6 series.
	for _, p := range []string{DiscoveryPlatformGreenhouse, DiscoveryPlatformLever, DiscoveryPlatformAshby} {
		for _, present := range []string{"true", "false"} {
			keys = append(keys, MetricHuntPostedAt+"{platform="+p+",present="+present+"}")
		}
	}
	// Fit-scoring filter counters (P6): pre-touch both stages so rate()-floor alerts
	// see 0 before the first ingest cycle. 2 stages = 2 series.
	for _, stage := range []string{scoreFilterRecency, scoreFilterJaccard} {
		keys = append(keys, MetricHuntScoreFiltered+"{stage="+stage+"}")
	}
	// Fit-scoring LLM result counters (P6): pre-touch all four results so
	// rate()-floor alerts see 0 before the first LLM call. 4 results = 4 series.
	for _, result := range []string{scoreLLMOk, scoreLLMEnumClamp, scoreLLMParseFail, scoreLLMError} {
		keys = append(keys, MetricHuntScoreLLM+"{result="+result+"}")
	}
	// Circuit breaker trips counter pre-touched so rate()-floor alerts see 0
	// before the first trip.
	keys = append(keys, MetricHuntScoreBreakerTrips)
	// Score persist failures counter pre-touched so rate()-floor alerts see 0
	// before the first failure.
	keys = append(keys, MetricHuntScorePersistFailures)
	// vacancy_ingest_total{result} pre-touched so rate()-floor alerts see 0
	// before the first operator call. 3 results = 3 series (bounded enum).
	for _, result := range []string{"ok", "weak", "skipped_store"} {
		keys = append(keys, MetricVacancyIngest+"{result="+result+"}")
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
func IncrToolCall()              { reg.Incr(MetricToolCalls) }

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
// outcome ∈ {"sent","failed","stale","no_date","low_fit","unscored"} — bounded label.
// "sent"/"failed" — emitted by ProductNotifier.dispatch via its OnSend hook.
// "stale"/"no_date" — emitted by ProductNotifier.NotifyNewJob recency gate.
// "low_fit" — emitted by huntworker.maybeNotifyJob fit gate (fit_score < MIN_FIT).
// "unscored" — emitted by huntworker.maybeNotifyJob for LLM-fail fail-open path.
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

// warmAlertBoundedMetrics pre-registers, at zero value, every label
// combination backing a Prometheus increase()-based alert rule in
// alerts-go-job.yml (GojobSourceParseFail, GojobSourceNoKey,
// GojobDelegationFallback). Called once from Init(), before any connector or
// discovery traffic.
//
// Why this is needed: increase() over a Prometheus counter cannot see a
// series' FIRST sample — the jump from "series does not exist" to "series
// exists at value N" is invisible to increase(), which only diffs between
// existing samples. Without pre-registration, the FIRST parse_fail / no_key
// outcome (or the first local-fallback) after every restart is silently
// missed by its alert; only a second occurrence in the same window produces
// a visible 0→N transition.
//
// reg.Add(name, 0) is the established "touch" pattern in this package (see
// IncrHuntDiscoveryURLs's n=0 case): it seeds the local atomic counter AND,
// via the prom bridge, calls CounterVec.WithLabelValues(...).Add(0), which
// creates and permanently registers the zero-value child series on the real
// Prometheus /metrics endpoint (promhttp over prometheus.DefaultRegisterer).
// This is distinct from — and fixes the gap left by — FormatMetrics's
// pre-touch loops, which only fake a zero via a Go map's missing-key default
// on the hand-rolled flat-text mirror; they never call reg.Add and so never
// touch the real registry Prometheus actually scrapes.
//
// Bounded: 18 platforms × 6 outcomes + 3 discovery sources = 111 series —
// safe cardinality (mirrors the bound already documented in FormatMetrics).
func warmAlertBoundedMetrics() {
	for p := range validPlatforms {
		for oc := range validPlatformOutcomes {
			reg.Add(MetricPlatformResults+"{platform="+p+",outcome="+oc+"}", 0)
		}
	}
	for src := range validDiscoverySources {
		reg.Add(MetricHuntDiscoverySource+"{source="+src+"}", 0)
	}
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

// ErrDiscoveryDegraded is returned (wrapped) by discovery.Client.DiscoverBoardURLs
// when raw_web_search responds with Degraded=true — meaning the result set is
// unreliable (all broad-web legs failed or the server-side context deadline fired).
// Callers use errors.Is(err, engine.ErrDiscoveryDegraded) to distinguish this
// class from a transport/connection error so they can emit a distinct metric label.
//
// Defined here (in engine, not in hunt/discovery) so the jobs package can import
// it without creating a cycle: jobs → discovery → engine → jobs.
var ErrDiscoveryDegraded = errors.New("discovery: raw_web_search degraded")

// validDiscoverySources bounds the source label for hunt_discovery_source_total.
// "go-search"        = fused multi-source path (Brave-API + ox-browser + DDG via go-search).
// "local-fallback"   = degraded DDG/Marginalia-only path (go-job's own SearchDirect);
//                      triggered by a transport/connection error from go-search.
// "degraded-fallback"= go-search returned HTTP 200 + Degraded=true (partial fan-out
//                      failure); also falls back to local but is observably distinct
//                      from a transport error and separable in dashboards/alerts.
var validDiscoverySources = map[string]bool{
	"go-search": true, "local-fallback": true, "degraded-fallback": true,
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
// source ∈ {"go-search","local-fallback","degraded-fallback"}. Unrecognised values
// are dropped silently (allowlist guard prevents cardinality explosion).
func IncrHuntDiscoverySource(source string) {
	if !validDiscoverySources[source] {
		return
	}
	reg.Incr(MetricHuntDiscoverySource + "{source=" + source + "}")
}

// validATSFetchErrorReasons bounds the reason label for ats_fetch_errors_total.
// reason ∈ {parse,truncated,status,transport}.
//   - parse:     json.Unmarshal failed on a well-sized body.
//   - truncated: io.LimitReader cap hit — body is incomplete, parse would fail.
//   - status:    non-200/non-404 HTTP status code from the ATS board API.
//   - transport: network error (timeout, connection refused, TLS, etc.).
var validATSFetchErrorReasons = map[string]bool{
	"parse":     true,
	"truncated": true,
	"status":    true,
	"transport": true,
}

// IncrATSFetchErrors bumps gojob_ats_fetch_errors_total{platform=<p>,reason=<r>}.
// platform ∈ {greenhouse,lever,ashby}, reason ∈ {parse,truncated,status,transport}.
// Unrecognised label values are silently dropped (cardinality guard).
// Called at every ATS board-fetch error exit so "discovered slug, board failed"
// is visible in Prometheus before the hunt_jobs table goes stale.
func IncrATSFetchErrors(platform, reason string) {
	if !validDiscoveryPlatforms[platform] || !validATSFetchErrorReasons[reason] {
		return
	}
	reg.Incr(MetricATSFetchErrors + "{platform=" + platform + ",reason=" + reason + "}")
}

// IncrHuntPostedAt bumps gojob_hunt_posted_at_total{platform=<p>,present=<b>}.
// platform ∈ {greenhouse,lever,ashby}; present is "true" when the ingested ATS row
// carried a parseable posted_at, "false" when it landed NULL. Unrecognised platform
// values are silently dropped (cardinality guard); the bool is normalised to the
// "true"/"false" string here so callers pass a plain bool.
func IncrHuntPostedAt(platform string, present bool) {
	if !validDiscoveryPlatforms[platform] {
		return
	}
	val := "false"
	if present {
		val = "true"
	}
	reg.Incr(MetricHuntPostedAt + "{platform=" + platform + ",present=" + val + "}")
}

// ObserveHuntCycleDuration records a worker cycle duration into
// gojob_hunt_cycle_duration_seconds.
func ObserveHuntCycleDuration(d float64) {
	reg.Observe(MetricHuntCycleDuration, d)
}

// validHuntScoreFilterStages bounds the stage label for hunt_score_filtered_total.
// stage ∈ {"recency","jaccard"} — the two pre-LLM drop points in the cascade scorer.
var validHuntScoreFilterStages = map[string]bool{scoreFilterRecency: true, scoreFilterJaccard: true}

// validHuntScoreLLMResults bounds the result label for hunt_score_llm_total.
// result ∈ {"ok","enum_clamp","parse_fail","llm_error"}.
// ok:         LLM returned valid parseable JSON with known enum values.
// enum_clamp: JSON parsed but an enum field was unknown and clamped.
// parse_fail: JSON could not be parsed (fail-open → unscored).
// llm_error:  LLM call itself failed (fail-open → unscored).
var validHuntScoreLLMResults = map[string]bool{
	scoreLLMOk: true, scoreLLMEnumClamp: true, scoreLLMParseFail: true, scoreLLMError: true,
}

// IncrHuntScoreFiltered bumps gojob_hunt_score_filtered_total{stage=<s>}.
// stage ∈ {scoreFilterRecency,scoreFilterJaccard} — bounded enum.
// Called by the worker after scoreJobWithLimit when the result short-circuited
// before the LLM (stale → recency, sub-Jaccard → jaccard).
// Unknown values are silently dropped (cardinality guard).
func IncrHuntScoreFiltered(stage string) {
	if !validHuntScoreFilterStages[stage] {
		return
	}
	reg.Incr(MetricHuntScoreFiltered + "{stage=" + stage + "}")
}

// IncrHuntScoreLLM bumps gojob_hunt_score_llm_total{result=<r>}.
// result ∈ {scoreLLMOk,scoreLLMEnumClamp,scoreLLMParseFail,scoreLLMError} — bounded enum.
// Called by the worker for any job that reached the LLM scorer stage.
// Unknown values are silently dropped (cardinality guard).
func IncrHuntScoreLLM(result string) {
	if !validHuntScoreLLMResults[result] {
		return
	}
	reg.Incr(MetricHuntScoreLLM + "{result=" + result + "}")
}

// IncrHuntScoreBreakerTrips bumps gojob_hunt_score_breaker_trips_total.
// Called by the worker when the LLM circuit breaker trips (budget exhausted).
func IncrHuntScoreBreakerTrips() {
	reg.Incr(MetricHuntScoreBreakerTrips)
}

// IncrHuntScorePersistFailures bumps gojob_hunt_score_persist_failures_total.
// Called by the worker when SetJobScore fails after all retry attempts.
func IncrHuntScorePersistFailures() {
	reg.Incr(MetricHuntScorePersistFailures)
}

// ObserveHuntFitScore records a single fit-score observation into the
// gojob_hunt_fit_score histogram. Called once per LLM-scored job (LLMCalled=true).
// Buckets are pre-configured via reg.RegisterHistogram in engine.Init().
func ObserveHuntFitScore(score int) {
	reg.Observe(MetricHuntFitScore, float64(score))
}

// validDiscoveryVariantResults bounds the result label for hunt_discovery_variants_total.
var validDiscoveryVariantResults = map[string]bool{"hit": true, "miss": true}

// validSlugCacheEvictionReasons bounds the reason label for hunt_slug_cache_evictions_total.
// lru   — LRU size-pressure eviction (maxSize exceeded in Merge)
// board_404 — HTTP 404/410 from board-fetch; slug confirmed gone
// ttl   — reserved for future periodic sweep; not emitted by current lazy-eviction impl
var validSlugCacheEvictionReasons = map[string]bool{"lru": true, "board_404": true, "ttl": true}

// IncrHuntDiscoveryVariant bumps gojob_hunt_discovery_variants_total{platform,result}.
// platform ∈ {greenhouse,lever,ashby}, result ∈ {hit,miss}.
// Unrecognised label values are silently dropped (cardinality guard).
func IncrHuntDiscoveryVariant(platform, result string) {
	if !validDiscoveryPlatforms[platform] || !validDiscoveryVariantResults[result] {
		return
	}
	reg.Incr(MetricHuntDiscoveryVariants + "{platform=" + platform + ",result=" + result + "}")
}

// IncrSlugCacheEviction bumps gojob_hunt_slug_cache_evictions_total{platform,reason}.
// platform ∈ {greenhouse,lever,ashby}, reason ∈ {lru,board_404,ttl}.
// lru=size-pressure, board_404=HTTP 404 from board-fetch, ttl=reserved.
// Unrecognised label values are silently dropped (cardinality guard).
func IncrSlugCacheEviction(platform, reason string) {
	if !validDiscoveryPlatforms[platform] || !validSlugCacheEvictionReasons[reason] {
		return
	}
	reg.Incr(MetricSlugCacheEvictions + "{platform=" + platform + ",reason=" + reason + "}")
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

// validVacancyIngestResults bounds the result label for vacancy_ingest_total.
var validVacancyIngestResults = map[string]bool{
	"ok":            true,
	"weak":          true,
	"skipped_store": true,
}

// SetHuntPersistEnabled sets gojob_hunt_persist_enabled to 1 (enabled) or 0 (disabled).
// Called from main.go after SetHuntStore (enabled) or in each fail-soft nil-store branch (disabled).
// No-op before engine.Init() (reg is nil; Gauge is nil-safe).
func SetHuntPersistEnabled(on bool) {
	if reg == nil {
		return
	}
	v := 0.0
	if on {
		v = 1.0
	}
	reg.Gauge(MetricHuntPersistEnabled).Set(v)
}

// IncrVacancyIngest bumps gojob_vacancy_ingest_total{result=<r>}.
// result ∈ {"ok","weak","skipped_store"} — bounded enum.
// Called once per vacancy_ingest call that reaches the extract step.
// Unknown values are silently dropped (cardinality guard).
func IncrVacancyIngest(result string) {
	if !validVacancyIngestResults[result] {
		return
	}
	reg.Incr(MetricVacancyIngest + "{result=" + result + "}")
}
