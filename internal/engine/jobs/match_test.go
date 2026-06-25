package jobs

// Tests for ScoreJobMatchCoverage.
//
// ScoreJobMatchCoverage computes overlap-coefficient = inter / min(|resumeKW|, |jobKW|)
// rather than the symmetric Jaccard (inter/union) used by ScoreJobMatch.
//
// Why coverage is better as a pre-filter pre-gate:
//   - Symmetric Jaccard is penalised by long job descriptions (large jobKW union)
//     even when the overlap is strong.
//   - Overlap coefficient measures "what fraction of the SMALLER set is shared"
//     which better answers "does this job match the resume?" without penalising
//     verbose JDs.
//
// The plan note at Decision 2 reads:
//   "a cleaner metric is overlap coefficient inter/min(|resumeKW|,|jobKW|)
//    or resume-coverage inter/|resumeKW|. Recommend ScoreJobMatchCoverage
//    in Phase 2; keep symmetric Jaccard only as the crude floor."

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestScoreJobMatchCoverage_PerfectOverlap verifies that when the resume and job
// texts are identical, coverage returns 100.
func TestScoreJobMatchCoverage_PerfectOverlap(t *testing.T) {
	text := "Go Rust PostgreSQL distributed systems"
	kw := extractMatchKW(text)
	score := ScoreJobMatchCoverage(kw, text)
	assert.InDelta(t, 100.0, score, 0.1, "identical texts must give coverage=100")
}

// TestScoreJobMatchCoverage_NoOverlap verifies zero coverage for disjoint texts.
func TestScoreJobMatchCoverage_NoOverlap(t *testing.T) {
	resumeKW := extractMatchKW("Go Rust PostgreSQL")
	score := ScoreJobMatchCoverage(resumeKW, "Java Spring Oracle")
	assert.InDelta(t, 0.0, score, 0.1, "disjoint texts must give coverage=0")
}

// TestScoreJobMatchCoverage_LongJD_NotPenalised tests the core correctness
// property: a long JD that happens to include the full resume keyword set
// must score 100 (resume fully covered), whereas symmetric ScoreJobMatch would
// return a low score because union dominates.
//
// Falsification: if we accidentally implemented symmetric Jaccard here, the
// long JD case would score ~11 instead of ~100 — test fails.
func TestScoreJobMatchCoverage_LongJD_NotPenalised(t *testing.T) {
	resumeText := "Go Rust PostgreSQL"
	// JD contains all resume keywords PLUS many more.
	longJDText := "Go Rust PostgreSQL Java Python Ruby Scala Haskell " +
		"Erlang Elixir Clojure Kotlin Swift TypeScript JavaScript PHP " +
		"MySQL Oracle MongoDB Redis Cassandra Elasticsearch Kafka Spark " +
		"Hadoop Flink Airflow Luigi Prefect Dask Ray TensorFlow PyTorch"

	resumeKW := extractMatchKW(resumeText)

	// Symmetric Jaccard would penalise this (large union).
	jaccard, _, _ := ScoreJobMatch(resumeKW, longJDText)
	// Coverage must be much higher — full resume match.
	coverage := ScoreJobMatchCoverage(resumeKW, longJDText)

	assert.Greater(t, coverage, jaccard,
		"coverage must be higher than symmetric Jaccard for long JD with full resume overlap")
	assert.InDelta(t, 100.0, coverage, 0.1,
		"all resume keywords appear in JD → coverage must be 100")
}

// TestScoreJobMatchCoverage_PartialOverlap verifies a specific partial case.
// Note: "go" is 2 chars and is filtered by extractMatchKW (min 3 chars rule).
//
// resumeKW from "Rust PostgreSQL Kubernetes": {rust, postgresql, kubernetes} = 3 items
// jobKW from "Rust Python Java distributed":  {rust, python, java, distributed} = 4 items
// inter = {rust} = 1
// min(3, 4) = 3
// coverage = 1/3 * 100 ≈ 33.3
func TestScoreJobMatchCoverage_PartialOverlap(t *testing.T) {
	resumeKW := extractMatchKW("Rust PostgreSQL Kubernetes")
	jobText := "Rust Python Java distributed"
	coverage := ScoreJobMatchCoverage(resumeKW, jobText)
	assert.InDelta(t, 33.3, coverage, 0.2)
}

// TestScoreJobMatchCoverage_EmptyResume verifies zero coverage when resume is empty.
func TestScoreJobMatchCoverage_EmptyResume(t *testing.T) {
	resumeKW := make(map[string]bool)
	score := ScoreJobMatchCoverage(resumeKW, "Go Rust PostgreSQL")
	assert.InDelta(t, 0.0, score, 0.1, "empty resume must give coverage=0")
}

// TestScoreJobMatchCoverage_EmptyJob verifies zero coverage when job text is empty.
func TestScoreJobMatchCoverage_EmptyJob(t *testing.T) {
	resumeKW := extractMatchKW("Go Rust PostgreSQL")
	score := ScoreJobMatchCoverage(resumeKW, "")
	assert.InDelta(t, 0.0, score, 0.1, "empty job text must give coverage=0")
}
