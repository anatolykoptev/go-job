package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// Metric name constants.
//
// All counters carry the `_total` suffix to comply with Prometheus naming
// conventions; the go-kit/metrics Prometheus bridge exposes them under the
// `gojob_` namespace (e.g. `gojob_search_requests_total`).
const (
	MetricSearchRequests = "search_requests_total"
	MetricLLMCalls       = "llm_calls_total"
	MetricLLMErrors      = "llm_errors_total"
	// OBS-6: LLM request latency histogram — makes LLM slowness visible
	// before it hits timeout. Buckets: 0.1s–60s (registered in Init).
	MetricLLMRequestDuration = "llm_request_duration_seconds"
	// OBS-6: oversize purge outcome counters — make table growth visible.
	MetricOversizePurgeDeleted = "oversize_purge_deleted_total"
	MetricOversizePurgeErrors  = "oversize_purge_errors_total"
	// OBS-6: enrichment semaphore skipped counter — makes "semaphore full"
	// events visible so operators can tune enrichMaxConcurrent.
	MetricEnrichSemSkipped = "enrich_semaphore_skipped_total"
	// OBS-6: admin UI HTTP metrics — request rate, error rate, latency.
	MetricAdminRequests         = "admin_requests_total"
	MetricAdminErrors           = "admin_errors_total"
	MetricAdminRequestDuration  = "admin_request_duration_seconds"
	MetricFetchRequests         = "fetch_requests_total"
	MetricFetchErrors           = "fetch_errors_total"
	MetricFreelancerAPIRequests = "freelancer_api_requests_total"
	MetricRemoteOKRequests      = "remoteok_requests_total"
	MetricWWRRequests           = "wwr_requests_total"
	MetricYouTubeSearchRequests = "youtube_search_requests_total"
	MetricYouTubeTranscriptReqs = "youtube_transcript_requests_total"
	MetricHNJobsRequests        = "hn_jobs_requests_total"
	MetricGreenhouseRequests    = "greenhouse_requests_total"
	MetricLeverRequests         = "lever_requests_total"
	MetricAshbyRequests         = "ashby_requests_total"
	MetricYCJobsRequests        = "yc_jobs_requests_total"
	MetricIndeedRequests        = "indeed_requests_total"
	MetricHabrRequests          = "habr_requests_total"
	MetricCraigslistRequests    = "craigslist_requests_total"
	// MetricCraigslistDiscoveryFallback is a labelled counter that fires when
	// the discovery fallback serves results AFTER the direct tiers (HTML/RSS)
	// were refused or errored. Without it, a permanently IP-blocked connector
	// reports outcome=ok via PlatformOutcome(n>0, nil) — the block is laundered
	// into a silent success (H2). reason ∈ {"blocked","failed","unmapped"}:
	//   - "blocked"  = direct tiers returned errCraigslistBlocked (anti-bot refusal)
	//   - "failed"   = direct tiers errored (transport/parse/timeout), not a block
	//   - "unmapped" = location not in craigslistRegions (never reached the network)
	// An alert on reason="blocked" contradicts the healthy outcome=ok and makes
	// the laundering observable.
	MetricCraigslistDiscoveryFallback = "craigslist_discovery_fallback_total"
	// MetricCraigslistDefaultLocation is the labelled counter
	// gojob_craigslist_default_location_total{tier}. Bumped ONCE per
	// craigslist search that substituted a location because the caller supplied
	// none — makes an invisible (and, per #347, potentially wrong-city)
	// substitution observable. tier ∈ {"profile","config","config_after_profile_error","config_after_profile_unmapped"}:
	//   - "profile" = the operator's resume_persons.location supplied the value;
	//   - "config"  = engine.Cfg.CraigslistDefaultLocation supplied it because
	//     the profile was empty or absent (a failed READ and an unmapped value
	//     each get their own tier below — do not fold them in here);
	//   - "config_after_profile_error" = config supplied it because the profile
	//     READ failed (saturated pool, ctx deadline);
	//   - "config_after_profile_unmapped" = config supplied it because the
	//     profile was non-empty but not an exact key/slug (round 6).
	// A rising profile rate with no results is the signal that the operator's
	// stored location does not map to a Craigslist area (resolveRegion returns
	// false → errCraigslistUnmapped) — visible now instead of as a silent
	// wrong-city or unexplained empty.
	MetricCraigslistDefaultLocation = "craigslist_default_location_total"
	MetricAlgoraRequests            = "algora_requests_total"
	MetricAlgoraJobsRequests        = "algora_jobs_requests_total"
	MetricSherlockRequests          = "sherlock_requests_total"
	MetricCantinaRequests           = "cantina_requests_total"
	MetricCode4renaRequests         = "code4rena_requests_total"
	MetricToolCalls                 = "tool_calls_total"

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
	scoreFilterQuality = "quality"

	// Fit-scoring LLM result labels (hunt_score_llm_total{result}).
	// Extracted to satisfy goconst (appear ≥3 times across allowlist + FormatMetrics).
	scoreLLMOk          = "ok"
	scoreLLMEnumClamp   = "enum_clamp"
	scoreLLMParseFail   = "parse_fail"
	scoreLLMError       = "llm_error"
	scoreLLMSkippedBudg = "skipped_budget"

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
	// decision. outcome ∈ {"sent", "failed", "stale", "no_date", "low_fit",
	// "unscored", "notifier_disabled"}.
	//
	// Alert thresholds:
	//   - rate(hunt_notify_total{outcome=low_fit}[10m]) > 0 → jobs dropped by fit
	//     gate — check HUNT_NOTIFY_MIN_FIT setting (may be set too high).
	//   - rate(hunt_notify_total{outcome=notifier_disabled}[5m]) > 0 → notifier
	//     is nil — Telegram bot not configured or init failed.
	//   - gojob_hunt_notify_health == 0 for >5m → bot token revoked/unreachable.
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

	// MetricSecurityFetchErrors is the labelled counter
	// gojob_security_fetch_errors_total{platform,reason}.
	// Bumped when a security bounty source fetch hits the read-cap DoS ceiling
	// (hackerone/bugcrowd/intigriti/yeswehack/federacy via security_bounty.go).
	// platform ∈ {hackerone,bugcrowd,intigriti,yeswehack,federacy},
	// reason ∈ {truncated}. Same shape as MetricATSFetchErrors — the truncation
	// signal that was previously only slog.Warn'd and swallowed when a sibling
	// source succeeded (fetchAllSecurityPrograms returns nil if any source
	// produced data), making the failure invisible for a month.
	MetricSecurityFetchErrors = "security_fetch_errors_total"

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

	// MetricHuntNotifyHealth is the gauge gojob_hunt_notify_health.
	// Set to 1 when the Telegram bot token is valid (health check passes),
	// 0 when the token is revoked or unreachable.
	// No labels — cardinality guard. Pre-touched at 1 in FormatMetrics.
	// Alert: gojob_hunt_notify_health == 0 for >5m → Telegram bot token invalid.
	MetricHuntNotifyHealth = "hunt_notify_health"

	// MetricHuntUnscoredJobsCount is the gauge gojob_hunt_unscored_jobs_count.
	// Set by the hunt worker's end-of-cycle unscored sweep to the number of open
	// jobs with scored_at IS NULL (aggregated from the UnscoredOpenJobs result).
	// No labels — cardinality guard. Pre-touched at 0 in FormatMetrics.
	// Alert: sustained non-zero + rising max-age → scoring pipeline stalled.
	MetricHuntUnscoredJobsCount = "hunt_unscored_jobs_count"

	// MetricHuntUnscoredJobsMaxAge is the gauge
	// gojob_hunt_unscored_jobs_max_age_seconds. Set by the hunt worker's unscored
	// sweep to the age (in seconds) of the oldest unscored open job.
	// No labels — cardinality guard. Pre-touched at 0 in FormatMetrics.
	// Alert: > 7200 (2h) for 5m → scoring freeze/stall (GojobHuntScoringFreezeStall).
	MetricHuntUnscoredJobsMaxAge = "hunt_unscored_jobs_max_age_seconds"

	// MetricHuntScoringDegraded is the gauge gojob_hunt_scoring_degraded.
	// 0 = healthy, 1 = degraded (circuit breaker open or fail-open path taken:
	// llm_error / parse_fail in scoreJobIfCreated). Reset to 0 at the start of
	// each hunt worker cycle. No labels — cardinality guard.
	// Pre-touched at 0 in FormatMetrics.
	// Alert: == 1 for 10m → GojobHuntScoringDegraded (silent_downgrade).
	MetricHuntScoringDegraded = "hunt_scoring_degraded"

	// MetricHuntScoringDegradedReason is the labelled counter
	// gojob_hunt_scoring_degraded_total{reason}.
	// reason ∈ {"breaker_open","llm_error","parse_fail"} — bounded enum.
	// Incremented each time the degraded gauge transitions 0→1, carrying the
	// cause. Budget exhaustion does NOT set the gauge (it is normal operation)
	// and is tracked separately via hunt_score_llm_total{result="skipped_budget"}.
	// Pre-touched for all reasons in FormatMetrics so rate()-floor alerts see 0.
	MetricHuntScoringDegradedReason = "hunt_scoring_degraded_total"

	// MetricRuntimeGoroutines is the gauge gojob_runtime_goroutines.
	// Updated every 15s with runtime.NumGoroutine(). No labels.
	// OBS-1 fix: goroutine leak monitoring — the codebase has 53+ `go func()`
	// spawns with no visibility into goroutine count. This gauge makes leaks
	// detectable before OOM.
	// Alert: rate(gojob_runtime_goroutines[5m]) > 10 for 5m → leak (warning).
	// Alert: gojob_runtime_goroutines > 10000 for 5m → critical.
	MetricRuntimeGoroutines = "runtime_goroutines"

	// MetricSlugCacheL2WriteErrors is the counter
	// gojob_slug_cache_l2_write_errors_total. Incremented when an L2 write
	// (Redis SET) fails. No labels — cardinality guard.
	// OBS-3 fix: L2 write failures were Debug-only logs, invisible in prod.
	// Alert: rate(gojob_slug_cache_l2_write_errors_total[5m]) > 0 → Redis down.
	MetricSlugCacheL2WriteErrors = "slug_cache_l2_write_errors_total"

	// MetricATSBreakerOpen is the counter gojob_ats_breaker_open_total.
	// Incremented when an ATS circuit breaker (ashby/greenhouse/lever) blocks
	// a fetch call. No labels — cardinality guard.
	// #180 fix: makes ATS breaker trips visible so operators can see when a
	// source is permanently blocked.
	// Alert: increase(gojob_ats_breaker_open_total[1h]) > 10 → ATS API down.
	MetricATSBreakerOpen = "ats_breaker_open_total"

	// BH-3 / OBS-5: DB pool stats gauges — make pool saturation detectable.
	MetricDBPoolConns      = "db_pool_connections_total"
	MetricDBPoolIdle       = "db_pool_idle_connections"
	MetricDBPoolAcquireSec = "db_pool_acquire_wait_seconds"

	// BH-12: slug cache L2 active gauge — 1=Redis connected, 0=degraded.
	MetricSlugCacheL2Active = "slug_cache_l2_active"

	// MetricHuntSourceLastSuccess is the labelled gauge
	// gojob_hunt_source_last_success_timestamp{kind,source}. Set to the current
	// unix timestamp whenever a scheduled ingest source yields at least one row
	// in a cycle. A source that has never succeeded is pre-touched at 0 so it is
	// distinguishable from a source with no series at all.
	//
	// This is the PRIMARY signal for detecting silently-dead sources: freshness
	// catches every failure mode (HTTP 200 empty, source dropped from fan-out,
	// parser yields zero) including modes nobody predicted. An error counter
	// can only count anticipated failure modes. All three month-long failures
	// (hackerone truncated read, himalayas field type, algora 404) would have
	// tripped a freshness alert on day one.
	//
	// kind ∈ validHuntSourceKinds, source ∈ registeredHuntSources[kind].
	// Pre-touched at 0 for every known source in warmAlertBoundedMetrics.
	MetricHuntSourceLastSuccess = "hunt_source_last_success_timestamp"

	// MetricHuntSourceOutcome is the labelled counter
	// gojob_hunt_source_outcome_total{kind,source,outcome}.
	// Emitted once per source per cycle from the scheduled fan-outs.
	// outcome ∈ {ok, empty, fetch_error, parse_error} — bounded enum.
	//
	// Complements freshness: separates "fetched fine, genuinely empty" from
	// "fetch failed" from "fetch succeeded, decode failed" — the distinction
	// that would have pointed at the right layer for all three real failures
	// instead of at the parser. fetch_error and parse_error are distinguishable
	// because two of the three real failures were mis-attributed (hackerone's
	// was a truncated read reported as a parse failure).
	//
	// kind ∈ validHuntSourceKinds, source ∈ registeredHuntSources[kind].
	// Pre-touched at 0 for every known source×outcome in warmAlertBoundedMetrics.
	MetricHuntSourceOutcome = "hunt_source_outcome_total"

	// MetricJobSearchRelevance is the labelled counter
	// gojob_job_search_relevance_total{outcome}. Bumped once per candidate in
	// the relevance-gate stage of runJobSearch. outcome ∈ {scored, kept,
	// floor_kept, rejected} — bounded enum. scored = cosine computed; kept =
	// passed the threshold gate; floor_kept = below threshold but retained by
	// the min-keep floor (a DISTINCT visible state from a real match — see B2);
	// rejected = below threshold and dropped. Pre-touched for all four outcomes
	// in FormatMetrics + warmAlertBoundedMetrics so rate()-floor alerts see 0
	// before the first job_search call.
	MetricJobSearchRelevance = "job_search_relevance_total"

	// MetricJobSearchRelevanceDegraded is the labelled counter
	// gojob_job_search_relevance_degraded_total{reason}. Bumped once per
	// job_search call that took the fail-open degraded path (embedder
	// unavailable). reason ∈ {not_configured, embed_error, circuit_open,
	// timeout, empty_vectors} — bounded enum, no free-form strings, no query
	// text. Pre-touched for all reasons so a rate()-floor alert sees 0 before
	// the first degradation.
	MetricJobSearchRelevanceDegraded = "job_search_relevance_degraded_total"

	// MetricJobSearchExtraction is the labelled counter
	// gojob_job_search_extraction_total{outcome}. Bumped once per
	// SummarizeJobResults call, classifying the LLM JSON parse outcome.
	// outcome ∈ {ok, trailing_garbage, schema_mismatch, truncated_salvaged,
	// unparseable} — bounded enum.
	//   - ok                  — full JSON parsed cleanly, all records kept
	//   - trailing_garbage    — full parse failed (trailing prose, missing brace)
	//     but the "jobs" array closed cleanly; NOT truncation, dropped = 0
	//   - schema_mismatch     — the array closed cleanly but one or more
	//     elements failed to unmarshal into JobListing (e.g. string where
	//     *int expected); the bad elements were skipped and the rest kept;
	//     dropped = exact count of skipped elements
	//   - truncated_salvaged  — full parse failed; the array (or enclosing
	//     object) was cut mid-record; complete records salvaged, truncated
	//     tail dropped
	//   - unparseable         — no complete records could be salvaged
	// Pre-touched for all outcomes so rate()-floor alerts see 0 before
	// the first job_search call.
	MetricJobSearchExtraction = "job_search_extraction_total"
)

// Extraction outcome label values (job_search_extraction_total{outcome}).
const (
	ExtractionOK                = "ok"
	ExtractionTrailingGarbage   = "trailing_garbage"
	ExtractionSchemaMismatch    = "schema_mismatch"
	ExtractionTruncatedSalvaged = "truncated_salvaged"
	ExtractionUnparseable       = "unparseable"
)

// validExtractionOutcomes bounds the outcome label for
// job_search_extraction_total. Unrecognised values are dropped silently
// (cardinality guard).
var validExtractionOutcomes = map[string]bool{
	ExtractionOK:                true,
	ExtractionTrailingGarbage:   true,
	ExtractionSchemaMismatch:    true,
	ExtractionTruncatedSalvaged: true,
	ExtractionUnparseable:       true,
}

// Relevance gate outcome label values (job_search_relevance_total{outcome}).
// Exported so the jobserver package can use them.
const (
	RelevanceScored    = "scored"
	RelevanceKept      = "kept"
	RelevanceFloorKept = "floor_kept"
	RelevanceRejected  = "rejected"
)

// validRelevanceOutcomes bounds the outcome label for job_search_relevance_total.
var validRelevanceOutcomes = map[string]bool{
	RelevanceScored: true, RelevanceKept: true, RelevanceFloorKept: true, RelevanceRejected: true,
}

// Relevance degraded reason label values (job_search_relevance_degraded_total{reason}).
// Exported so the jobserver package can use them in the output summary.
const (
	RelevanceReasonNotConfigured = "not_configured"
	RelevanceReasonEmbedError    = "embed_error"
	RelevanceReasonCircuitOpen   = "circuit_open"
	RelevanceReasonTimeout       = "timeout"
	RelevanceReasonEmptyVectors  = "empty_vectors"
	RelevanceReasonTruncated     = "truncated"
)

// validRelevanceDegradedReasons bounds the reason label for
// job_search_relevance_degraded_total. Unrecognised values are dropped silently
// (cardinality guard — no free-form strings, no query text).
var validRelevanceDegradedReasons = map[string]bool{
	RelevanceReasonNotConfigured: true,
	RelevanceReasonEmbedError:    true,
	RelevanceReasonCircuitOpen:   true,
	RelevanceReasonTimeout:       true,
	RelevanceReasonEmptyVectors:  true,
	RelevanceReasonTruncated:     true,
}

// Source outcome label values for gojob_hunt_source_outcome_total.
const (
	sourceOutcomeOk         = "ok"
	sourceOutcomeEmpty      = "empty"
	sourceOutcomeFetchError = "fetch_error"
	sourceOutcomeParseError = "parse_error"
)

// validHuntSourceOutcomes bounds the outcome label for
// hunt_source_outcome_total. Unrecognised values are rejected.
var validHuntSourceOutcomes = map[string]bool{
	sourceOutcomeOk: true, sourceOutcomeEmpty: true,
	sourceOutcomeFetchError: true, sourceOutcomeParseError: true,
}

// validHuntSourceKinds bounds the kind label for the hunt source metrics.
// Distinct from validHuntKinds (which is for hunt_ingest_total and includes
// "job" and "audit_contest"). The source metrics cover only the three
// opportunity fan-out kinds.
var validHuntSourceKinds = map[string]bool{
	"bounty": true, "security": true, "freelance": true,
}

// registeredHuntSources maps kind → slice of source names, populated via
// RegisterHuntSources from the jobs package at init time. This is the
// SINGLE source of truth for the source label set — derived from the real
// fan-out tables in jobs, NOT a hand-maintained list. A new source added to
// a fan-out table is automatically included here and picked up by
// warmAlertBoundedMetrics pre-touch + FormatMetrics pre-touch.
var registeredHuntSources = map[string][]string{}

// RegisterHuntSources wires the per-kind source name lists from the jobs
// package into the engine package without creating an import cycle
// (engine → jobs would cycle since jobs → engine). Called once from
// jobs.init() — before main() — so the lists are available when
// engine.Init() → warmAlertBoundedMetrics() runs.
func RegisterHuntSources(sources map[string][]string) {
	registeredHuntSources = sources
}

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

// LLMRequestDurationBuckets cover the latency range of LLM calls:
// fast (0.1s) through slow (60s). OBS-6: makes LLM slowness visible
// before it hits timeout.
var LLMRequestDurationBuckets = []float64{0.1, 0.5, 1, 2, 5, 10, 30, 60}

// AdminRequestDurationBuckets cover admin UI response latency.
// OBS-6: range 1ms–10s — admin pages are fast DB queries or htmx partials.
var AdminRequestDurationBuckets = []float64{0.001, 0.01, 0.05, 0.1, 0.5, 1, 5, 10}

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

// GetGaugeValue returns the current value of a gauge metric by name.
// Returns 0 if the registry is not initialised or the gauge has not been set.
// Exported for test verification from sub-packages (e.g. huntworker).
func GetGaugeValue(name string) float64 {
	if reg == nil {
		return 0
	}
	return reg.GaugeSnapshot()[name]
}

// FormatMetrics returns metrics as a simple text format for HTTP endpoint.
func FormatMetrics() string {
	m := GetMetrics()
	keys := []string{
		MetricSearchRequests, MetricLLMCalls, MetricLLMErrors,
		MetricFetchRequests, MetricFetchErrors,
		MetricFreelancerAPIRequests,
		MetricRemoteOKRequests, MetricWWRRequests,
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
	// H2: craigslist discovery-fallback counter pre-touched so a block-laundered
	// outcome=ok is visible to rate()-floor alerts before the first fire.
	for _, r := range []string{"blocked", "failed", "unmapped"} {
		keys = append(keys, MetricCraigslistDiscoveryFallback+"{reason="+r+"}")
	}
	// craigslist_default_location_total{tier} pre-touched so a rate()-floor
	// alert sees 0 before the first substituted-location search, and so the
	// profile vs config split is visible from the first run. Ranges over
	// validCraigslistDefaultLocationTiers (the same map warmAlertBoundedMetrics
	// uses) so a future tier cannot be added to one site only.
	for _, tier := range sortedCraigslistDefaultLocationTiers() {
		keys = append(keys, MetricCraigslistDefaultLocation+"{tier="+tier+"}")
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
	// outcome ∈ {"sent","failed","stale","no_date","low_fit","unscored","notifier_disabled"}.
	// "low_fit" — fit gate dropped the job (fit_score < HUNT_NOTIFY_MIN_FIT).
	// "unscored" — LLM scorer failed, notified with degraded card (fail-open).
	// "notifier_disabled" — notifier is nil (bot init failed or token missing).
	for _, oc := range []string{"sent", "failed", "stale", "no_date", "low_fit", "unscored", "notifier_disabled"} {
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
	// Fit-scoring filter counters (P6): pre-touch all stages so rate()-floor alerts
	// see 0 before the first ingest cycle. 3 stages = 3 series.
	for _, stage := range []string{scoreFilterRecency, scoreFilterJaccard, scoreFilterQuality} {
		keys = append(keys, MetricHuntScoreFiltered+"{stage="+stage+"}")
	}
	// Fit-scoring LLM result counters (P6): pre-touch all five results so
	// rate()-floor alerts see 0 before the first LLM call. 5 results = 5 series.
	for _, result := range []string{scoreLLMOk, scoreLLMEnumClamp, scoreLLMParseFail, scoreLLMError, scoreLLMSkippedBudg} {
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
	// ESC-2 observability gauges pre-touched at 0 so they appear on the flat
	// text endpoint before the first sweep/cycle. No labels — single series each.
	keys = append(keys, MetricHuntUnscoredJobsCount)
	keys = append(keys, MetricHuntUnscoredJobsMaxAge)
	keys = append(keys, MetricHuntScoringDegraded)
	// Degraded reason counters pre-touched so rate()-floor alerts see 0.
	// 3 reasons = 3 series (bounded enum).
	for _, r := range []string{"breaker_open", "llm_error", "parse_fail"} {
		keys = append(keys, MetricHuntScoringDegradedReason+"{reason="+r+"}")
	}
	// OBS-1: goroutine count gauge pre-touched at 0.
	keys = append(keys, MetricRuntimeGoroutines)
	// OBS-3: L2 write error counter pre-touched at 0.
	keys = append(keys, MetricSlugCacheL2WriteErrors)
	// #180: ATS breaker open counter pre-touched at 0.
	keys = append(keys, MetricATSBreakerOpen)
	// BH-3 / OBS-5: DB pool stats gauges pre-touched at 0.
	keys = append(keys, MetricDBPoolConns, MetricDBPoolIdle, MetricDBPoolAcquireSec)
	// BH-12: slug cache L2 active gauge pre-touched at 0.
	keys = append(keys, MetricSlugCacheL2Active)
	// OBS-6: oversize purge counters pre-touched at 0.
	keys = append(keys, MetricOversizePurgeDeleted, MetricOversizePurgeErrors)
	// OBS-6: enrichment semaphore skipped counter pre-touched at 0.
	keys = append(keys, MetricEnrichSemSkipped)
	// OBS-6: admin UI HTTP counters pre-touched at 0.
	keys = append(keys, MetricAdminRequests, MetricAdminErrors)
	// Hunt source freshness gauges + outcome counters pre-touched at 0 for
	// every known source, so a source that has never succeeded is visible as
	// a 0-value series (not missing) and rate()-floor alerts see 0 before the
	// first cycle. Source list derived from registeredHuntSources (populated
	// by jobs.init() from the real fan-out tables).
	for kind, sources := range registeredHuntSources {
		for _, src := range sources {
			keys = append(keys, MetricHuntSourceLastSuccess+"{kind="+kind+",source="+src+"}")
			for _, oc := range []string{sourceOutcomeOk, sourceOutcomeEmpty, sourceOutcomeFetchError, sourceOutcomeParseError} {
				keys = append(keys, MetricHuntSourceOutcome+"{kind="+kind+",source="+src+",outcome="+oc+"}")
			}
		}
	}
	// Relevance gate counters pre-touched so rate()-floor alerts see 0 before
	// the first job_search call. 4 outcomes + 6 degraded reasons = 10 series.
	for _, oc := range []string{RelevanceScored, RelevanceKept, RelevanceFloorKept, RelevanceRejected} {
		keys = append(keys, MetricJobSearchRelevance+"{outcome="+oc+"}")
	}
	for _, r := range []string{RelevanceReasonNotConfigured, RelevanceReasonEmbedError, RelevanceReasonCircuitOpen, RelevanceReasonTimeout, RelevanceReasonEmptyVectors, RelevanceReasonTruncated} {
		keys = append(keys, MetricJobSearchRelevanceDegraded+"{reason="+r+"}")
	}
	// Extraction outcome counters pre-touched so rate()-floor alerts see 0
	// before the first job_search call. 4 outcomes = 4 series (bounded enum).
	for _, oc := range []string{ExtractionOK, ExtractionTrailingGarbage, ExtractionTruncatedSalvaged, ExtractionUnparseable} {
		keys = append(keys, MetricJobSearchExtraction+"{outcome="+oc+"}")
	}

	var sb strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&sb, "%s %d\n", k, m[k])
	}
	return sb.String()
}

// Job-domain metric incrementors for sub-packages.

func IncrHNJobsRequests()     { reg.Incr(MetricHNJobsRequests) }
func IncrGreenhouseRequests() { reg.Incr(MetricGreenhouseRequests) }
func IncrLeverRequests()      { reg.Incr(MetricLeverRequests) }
func IncrAshbyRequests()      { reg.Incr(MetricAshbyRequests) }
func IncrYCJobsRequests()     { reg.Incr(MetricYCJobsRequests) }
func IncrRemoteOKRequests()   { reg.Incr(MetricRemoteOKRequests) }
func IncrWWRRequests()        { reg.Incr(MetricWWRRequests) }
func IncrIndeedRequests()     { reg.Incr(MetricIndeedRequests) }
func IncrHabrRequests()       { reg.Incr(MetricHabrRequests) }
func IncrCraigslistRequests() { reg.Incr(MetricCraigslistRequests) }

// validCraigslistDiscoveryReasons bounds the reason label for
// craigslist_discovery_fallback_total. "blocked" = direct tiers returned
// errCraigslistBlocked; "failed" = direct tiers errored (not a block);
// "unmapped" = location not in craigslistRegions (never reached the network).
var validCraigslistDiscoveryReasons = map[string]bool{
	"blocked": true, "failed": true, "unmapped": true,
}

// IncrCraigslistDiscoveryFallback bumps
// gojob_craigslist_discovery_fallback_total{reason=<r>}. reason ∈
// {"blocked","failed","unmapped"}. Unrecognised values are dropped silently (allowlist
// guard prevents cardinality explosion). Called when the discovery fallback
// serves results after the direct tiers were refused/errored — makes a
// block-laundered outcome=ok observable (H2).
func IncrCraigslistDiscoveryFallback(reason string) {
	if !validCraigslistDiscoveryReasons[reason] {
		return
	}
	reg.Incr(MetricCraigslistDiscoveryFallback + "{reason=" + reason + "}")
}

// validCraigslistDefaultLocationTiers bounds the tier label for
// craigslist_default_location_total. "profile" = resume_persons.location
// supplied the value; "config" = engine.Cfg.CraigslistDefaultLocation supplied
// it (profile empty/missing); "config_after_profile_error" = the config value
// supplied it because the profile READ failed (saturated pool, ctx deadline) —
// distinct from "config" so a chronically saturated pool is not hidden behind
// the same label as a no-profile deployment; "config_after_profile_unmapped" =
// the profile was non-empty but not an exact key/slug, so the config tier
// rescued it (round 6). Unrecognised values are dropped silently (cardinality
// guard).
var validCraigslistDefaultLocationTiers = map[string]bool{
	"profile": true, "config": true, "config_after_profile_error": true,
	"config_after_profile_unmapped": true,
}

// sortedCraigslistDefaultLocationTiers returns the keys of
// validCraigslistDefaultLocationTiers in sorted order, so FormatMetrics
// renders a deterministic series list regardless of Go's randomised map
// iteration. Mirrors the map-range warmAlertBoundedMetrics already does.
func sortedCraigslistDefaultLocationTiers() []string {
	tiers := make([]string, 0, len(validCraigslistDefaultLocationTiers))
	for tier := range validCraigslistDefaultLocationTiers {
		tiers = append(tiers, tier)
	}
	sort.Strings(tiers)
	return tiers
}

// IncrCraigslistDefaultLocation bumps
// gojob_craigslist_default_location_total{tier=<t>}. tier ∈ {"profile","config","config_after_profile_error","config_after_profile_unmapped"}.
// Called once per craigslist search that substituted a location because the
// caller supplied none — makes the substitution (and which tier supplied it)
// observable, so a wrong-city default (#347) is diagnosable instead of silent.
func IncrCraigslistDefaultLocation(tier string) {
	if !validCraigslistDefaultLocationTiers[tier] {
		return
	}
	reg.Incr(MetricCraigslistDefaultLocation + "{tier=" + tier + "}")
}
func IncrFreelancerAPIRequests() { reg.Incr(MetricFreelancerAPIRequests) }
func IncrAlgoraRequests()        { reg.Incr(MetricAlgoraRequests) }
func IncrAlgoraJobsRequests()    { reg.Incr(MetricAlgoraJobsRequests) }
func IncrSherlockRequests()      { reg.Incr(MetricSherlockRequests) }
func IncrCantinaRequests()       { reg.Incr(MetricCantinaRequests) }
func IncrCode4renaRequests()     { reg.Incr(MetricCode4renaRequests) }
func IncrYouTubeSearch()         { reg.Incr(MetricYouTubeSearchRequests) }
func IncrYouTubeTranscript()     { reg.Incr(MetricYouTubeTranscriptReqs) }
func IncrToolCall()              { reg.Incr(MetricToolCalls) }

// AddJobSearchRelevance adds n to gojob_job_search_relevance_total{outcome=<o>}
// in one reg.Add call, for the per-batch counts (scored/kept/floor_kept/rejected)
// the gate derives from slice lengths — avoids a per-candidate Incr loop.
func AddJobSearchRelevance(outcome string, n int) {
	if n <= 0 || !validRelevanceOutcomes[outcome] {
		return
	}
	reg.Add(MetricJobSearchRelevance+"{outcome="+outcome+"}", int64(n))
}

// IncrJobSearchRelevanceDegraded bumps
// gojob_job_search_relevance_degraded_total{reason=<r>}. reason ∈
// {not_configured, embed_error, circuit_open, timeout, empty_vectors, truncated}
// — bounded label. Called once per job_search call that took the fail-open
// degraded path, or once when the candidate cap trims (truncated — the gate
// still runs on the capped set, so it is NOT fail-open, but the coverage gap is
// made observable).
func IncrJobSearchRelevanceDegraded(reason string) {
	if !validRelevanceDegradedReasons[reason] {
		return
	}
	reg.Incr(MetricJobSearchRelevanceDegraded + "{reason=" + reason + "}")
}

// IncrJobSearchExtraction bumps gojob_job_search_extraction_total{outcome=<o>}.
// outcome ∈ {ok, trailing_garbage, schema_mismatch, truncated_salvaged,
// unparseable} — bounded enum. Called once per SummarizeJobResults call.
func IncrJobSearchExtraction(outcome string) {
	if !validExtractionOutcomes[outcome] {
		return
	}
	reg.Incr(MetricJobSearchExtraction + "{outcome=" + outcome + "}")
}

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
// outcome ∈ {"sent","failed","stale","no_date","low_fit","unscored","notifier_disabled"} — bounded label.
// "sent"/"failed" — emitted by ProductNotifier.dispatch via its OnSend hook.
// "stale"/"no_date" — emitted by ProductNotifier.NotifyNewJob recency gate.
// "low_fit" — emitted by huntworker.maybeNotifyJob fit gate (fit_score < MIN_FIT).
// "unscored" — emitted by huntworker.maybeNotifyJob for LLM-fail fail-open path.
// "notifier_disabled" — emitted by huntworker.maybeNotifyJob when notifier is nil.
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
	// H2: pre-register craigslist_discovery_fallback_total{reason} so the
	// FIRST block-laundered outcome=ok after a restart is visible to
	// increase()-based alerts (same gap warmAlertBoundedMetrics fixes for
	// parse_fail/no_key).
	for reason := range validCraigslistDiscoveryReasons {
		reg.Add(MetricCraigslistDiscoveryFallback+"{reason="+reason+"}", 0)
	}
	// Pre-register hunt_source_outcome_total{kind,source,outcome} and
	// hunt_source_last_success_timestamp{kind,source} at 0 for every known
	// source, so a source that has never succeeded is distinguishable from
	// a source with no series at all, and increase()-based alerts see a real
	// 0→N transition on the first outcome. The source list is derived from
	// the real fan-out tables via registeredHuntSources (populated by
	// jobs.init() before main() runs).
	for kind, sources := range registeredHuntSources {
		for _, src := range sources {
			reg.Gauge(MetricHuntSourceLastSuccess + "{kind=" + kind + ",source=" + src + "}").Set(0)
			for _, oc := range []string{sourceOutcomeOk, sourceOutcomeEmpty, sourceOutcomeFetchError, sourceOutcomeParseError} {
				reg.Add(MetricHuntSourceOutcome+"{kind="+kind+",source="+src+",outcome="+oc+"}", 0)
			}
		}
	}
	// Pre-register craigslist_default_location_total{tier} so the FIRST
	// substituted-location search after a restart is visible to increase()-
	// based alerts (same gap warmAlertBoundedMetrics fixes for the discovery
	// fallback counter).
	for tier := range validCraigslistDefaultLocationTiers {
		reg.Add(MetricCraigslistDefaultLocation+"{tier="+tier+"}", 0)
	}
	// Pre-register job_search_relevance_total{outcome} and
	// job_search_relevance_degraded_total{reason} so the FIRST relevance-gate
	// event after a restart is visible to increase()-based alerts (same gap
	// warmAlertBoundedMetrics fixes for platform_results / discovery).
	for oc := range validRelevanceOutcomes {
		reg.Add(MetricJobSearchRelevance+"{outcome="+oc+"}", 0)
	}
	for reason := range validRelevanceDegradedReasons {
		reg.Add(MetricJobSearchRelevanceDegraded+"{reason="+reason+"}", 0)
	}
	// Pre-register job_search_extraction_total{outcome} so the FIRST
	// truncation-salvage or unparseable event after a restart is visible to
	// increase()-based alerts (same gap warmAlertBoundedMetrics fixes for
	// the relevance counters).
	for oc := range validExtractionOutcomes {
		reg.Add(MetricJobSearchExtraction+"{outcome="+oc+"}", 0)
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
//
//	triggered by a transport/connection error from go-search.
//
// "degraded-fallback"= go-search returned HTTP 200 + Degraded=true (partial fan-out
//
//	failure); also falls back to local but is observably distinct
//	from a transport error and separable in dashboards/alerts.
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

// validSecurityPlatforms bounds the platform label for security_fetch_errors_total.
// platform ∈ {hackerone,bugcrowd,intigriti,yeswehack,federacy}.
var validSecurityPlatforms = map[string]bool{
	"hackerone": true,
	"bugcrowd":  true,
	"intigriti": true,
	"yeswehack": true,
	"federacy":  true,
}

// validSecurityFetchErrorReasons bounds the reason label for
// security_fetch_errors_total. reason ∈ {truncated}.
var validSecurityFetchErrorReasons = map[string]bool{
	"truncated": true,
}

// IncrSecurityFetchErrors bumps gojob_security_fetch_errors_total{platform=<p>,reason=<r>}.
// platform ∈ {hackerone,bugcrowd,intigriti,yeswehack,federacy}, reason ∈ {truncated}.
// Unrecognised label values are silently dropped (cardinality guard).
// Same shape as IncrATSFetchErrors — makes a truncation that was previously
// only slog.Warn'd (and swallowed when a sibling source succeeded) visible in
// Prometheus.
func IncrSecurityFetchErrors(platform, reason string) {
	if !validSecurityPlatforms[platform] || !validSecurityFetchErrorReasons[reason] {
		return
	}
	reg.Incr(MetricSecurityFetchErrors + "{platform=" + platform + ",reason=" + reason + "}")
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
// stage ∈ {"recency","jaccard","quality"} — the three pre-LLM drop points in the cascade scorer.
var validHuntScoreFilterStages = map[string]bool{scoreFilterRecency: true, scoreFilterJaccard: true, scoreFilterQuality: true}

// validHuntScoreLLMResults bounds the result label for hunt_score_llm_total.
// result ∈ {"ok","enum_clamp","parse_fail","llm_error","skipped_budget"}.
// ok:            LLM returned valid parseable JSON with known enum values.
// enum_clamp:    JSON parsed but an enum field was unknown and clamped.
// parse_fail:    JSON could not be parsed (fail-open → unscored).
// llm_error:     LLM call itself failed (fail-open → unscored).
// skipped_budget: per-cycle LLM cap reached before this job could be scored.
var validHuntScoreLLMResults = map[string]bool{
	scoreLLMOk: true, scoreLLMEnumClamp: true, scoreLLMParseFail: true, scoreLLMError: true, scoreLLMSkippedBudg: true,
}

// IncrHuntScoreFiltered bumps gojob_hunt_score_filtered_total{stage=<s>}.
// stage ∈ {scoreFilterRecency,scoreFilterJaccard,scoreFilterQuality} — bounded enum.
// Called by the worker after scoreJobWithLimit when the result short-circuited
// before the LLM (stale → recency, sub-Jaccard → jaccard, low-quality → quality).
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

// SetHuntNotifyHealth sets gojob_hunt_notify_health to 1 (healthy) or 0 (unhealthy).
// Called from main.go after the initial notifier setup (optimistic true) and
// by the periodic health check goroutine. No-op before engine.Init() (reg is
// nil; Gauge is nil-safe).
func SetHuntNotifyHealth(healthy bool) {
	if reg == nil {
		return
	}
	v := 0.0
	if healthy {
		v = 1.0
	}
	reg.Gauge(MetricHuntNotifyHealth).Set(v)
}

// SetHuntUnscoredJobsCount sets gojob_hunt_unscored_jobs_count to val.
// Called by the hunt worker's unscored sweep after fetching UnscoredOpenJobs
// (aggregated from the result in Go — no extra SQL query).
// No-op before engine.Init() (reg is nil; Gauge is nil-safe).
func SetHuntUnscoredJobsCount(val float64) {
	if reg == nil {
		return
	}
	reg.Gauge(MetricHuntUnscoredJobsCount).Set(val)
}

// SetHuntUnscoredJobsMaxAge sets gojob_hunt_unscored_jobs_max_age_seconds to val.
// Called by the hunt worker's unscored sweep with the age (in seconds) of the
// oldest unscored open job. No-op before engine.Init() (reg is nil; Gauge is nil-safe).
func SetHuntUnscoredJobsMaxAge(val float64) {
	if reg == nil {
		return
	}
	reg.Gauge(MetricHuntUnscoredJobsMaxAge).Set(val)
}

// validHuntScoringDegradedReasons bounds the reason label for
// hunt_scoring_degraded_total{reason}. Bounded enum — never a raw error string.
var validHuntScoringDegradedReasons = map[string]bool{
	"breaker_open": true, "llm_error": true, "parse_fail": true,
}

// scoringDegradedState tracks the current gauge value so SetHuntScoringDegraded
// can detect transitions and log/increment only on actual 0↔1 changes.
var scoringDegradedState atomic.Bool

// SetHuntScoringDegraded sets gojob_hunt_scoring_degraded to 1 (degraded) or
// 0 (healthy) and logs the transition with the reason. Called by the hunt
// worker: reset to 0 at cycle start (reason="cycle_reset"), set to 1 when the
// circuit breaker trips (reason="breaker_open") or the fail-open path is taken
// (reason="llm_error" or "parse_fail" in scoreJobIfCreated). No-op before
// engine.Init() (reg is nil). Only actual 0↔1 transitions produce a log line
// and counter increment; redundant calls with the same value are silent.
func SetHuntScoringDegraded(degraded bool, reason string) {
	prev := scoringDegradedState.Swap(degraded)
	if prev == degraded {
		return
	}
	if reg == nil {
		return
	}
	v := 0.0
	if degraded {
		v = 1.0
		slog.Warn("hunt scoring: entering degraded mode", slog.String("reason", reason))
		IncrHuntScoringDegradedReason(reason)
	} else {
		slog.Info("hunt scoring: leaving degraded mode", slog.String("reason", reason))
	}
	reg.Gauge(MetricHuntScoringDegraded).Set(v)
}

// IncrHuntScoringDegradedReason bumps gojob_hunt_scoring_degraded_total{reason=<r>}.
// reason ∈ {"breaker_open","llm_error","parse_fail"} — bounded enum. Unknown
// values are silently dropped (cardinality guard).
func IncrHuntScoringDegradedReason(reason string) {
	if reg == nil || !validHuntScoringDegradedReasons[reason] {
		return
	}
	reg.Incr(MetricHuntScoringDegradedReason + "{reason=" + reason + "}")
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

// StartGoroutineCollector launches a background goroutine that updates
// gojob_runtime_goroutines every 15s with runtime.NumGoroutine().
// OBS-1 fix: makes goroutine leaks detectable before OOM.
// No-op if the metrics registry is not initialised.
func StartGoroutineCollector(ctx context.Context) {
	if reg == nil {
		return
	}
	// Set initial value immediately.
	reg.Gauge(MetricRuntimeGoroutines).Set(float64(runtime.NumGoroutine()))
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				reg.Gauge(MetricRuntimeGoroutines).Set(float64(runtime.NumGoroutine()))
			}
		}
	}()
}

// IncrSlugCacheL2WriteError bumps gojob_slug_cache_l2_write_errors_total.
// OBS-3 fix: L2 write failures were Debug-only logs, invisible in production.
// Called by the slug cache L2 writer when Redis SET fails.
func IncrSlugCacheL2WriteError() {
	reg.Incr(MetricSlugCacheL2WriteErrors)
}

// IncrATSBreakerOpen bumps gojob_ats_breaker_open_total.
// #180 fix: makes ATS circuit breaker trips visible in Prometheus.
// Called by fetchGreenhouseJobs/fetchLeverPostings/fetchAshbyJobs when the
// breaker is open and blocks the call.
func IncrATSBreakerOpen() {
	reg.Incr(MetricATSBreakerOpen)
}

// OBS-6: oversize purge outcome incrementors.
func IncrOversizePurgeDeleted(n int64) { reg.Add(MetricOversizePurgeDeleted, n) }
func IncrOversizePurgeErrors()         { reg.Incr(MetricOversizePurgeErrors) }

// OBS-6: enrichment semaphore skipped — bumped when enrichSem is full.
func IncrEnrichSemSkipped() { reg.Incr(MetricEnrichSemSkipped) }

// AdminMetricsMiddleware wraps an http.Handler with request count, error count,
// and latency histogram. OBS-6: makes admin UI traffic and error rate visible.
// Status code is captured via a responseWriter wrapper.
func AdminMetricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reg.Incr(MetricAdminRequests)
		timer := reg.StartTimer(MetricAdminRequestDuration)
		defer timer.Stop()
		rw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(rw, r)
		if rw.status >= 500 {
			reg.Incr(MetricAdminErrors)
		}
	})
}

// statusWriter wraps http.ResponseWriter to capture the status code.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// StartDBPoolCollector launches a background goroutine that updates DB pool
// stats gauges every 15s from pgxpool.Stat(). BH-3 / OBS-5: makes pool
// saturation and acquire wait time detectable before cascading timeouts.
// No-op if the metrics registry is not initialised.
func StartDBPoolCollector(ctx context.Context, poolStat func() PoolStatSnapshot) {
	if reg == nil || poolStat == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s := poolStat()
				reg.Gauge(MetricDBPoolConns).Set(float64(s.TotalConns))
				reg.Gauge(MetricDBPoolIdle).Set(float64(s.IdleConns))
				if s.AcquireCount > 0 {
					reg.Gauge(MetricDBPoolAcquireSec).Set(s.AcquireDuration / float64(s.AcquireCount))
				}
			}
		}
	}()
}

// PoolStatSnapshot is a snapshot of pgxpool.Stat() values for the collector.
type PoolStatSnapshot struct {
	TotalConns      int32
	IdleConns       int32
	AcquireCount    int64
	AcquireDuration float64 // seconds
}

// SetSlugCacheL2Active sets gojob_slug_cache_l2_active to 1 (Redis connected)
// or 0 (degraded/in-process only). BH-12: makes Redis unavailability visible.
func SetSlugCacheL2Active(active bool) {
	if reg == nil {
		return
	}
	var v float64
	if active {
		v = 1
	}
	reg.Gauge(MetricSlugCacheL2Active).Set(v)
}

// IncrHuntSourceOutcome bumps gojob_hunt_source_outcome_total{kind,source,outcome}.
// kind ∈ validHuntSourceKinds, outcome ∈ validHuntSourceOutcomes — both bounded.
// source is validated against the registered source list for the given kind;
// unrecognised kind/source/outcome values are silently dropped (cardinality guard).
// Called once per source per cycle from the scheduled ingest fan-outs.
func IncrHuntSourceOutcome(kind, source, outcome string) {
	if reg == nil || !validHuntSourceKinds[kind] || !validHuntSourceOutcomes[outcome] {
		return
	}
	if !isRegisteredHuntSource(kind, source) {
		return
	}
	reg.Incr(MetricHuntSourceOutcome + "{kind=" + kind + ",source=" + source + ",outcome=" + outcome + "}")
}

// SetHuntSourceLastSuccess sets gojob_hunt_source_last_success_timestamp{kind,source}
// to the current unix time. Called when a source yields at least one row in a
// scheduled cycle. No-op before engine.Init() (reg is nil; Gauge is nil-safe).
func SetHuntSourceLastSuccess(kind, source string) {
	if reg == nil || !validHuntSourceKinds[kind] {
		return
	}
	if !isRegisteredHuntSource(kind, source) {
		return
	}
	reg.Gauge(MetricHuntSourceLastSuccess + "{kind=" + kind + ",source=" + source + "}").Set(float64(time.Now().Unix()))
}

// isRegisteredHuntSource returns true if source is in the registered list for kind.
func isRegisteredHuntSource(kind, source string) bool {
	for _, s := range registeredHuntSources[kind] {
		if s == source {
			return true
		}
	}
	return false
}
