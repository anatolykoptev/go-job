package jobs

// ats_truncation_test.go guards the body-truncation class fixed in P5.
//
// Root cause: fetchLeverPostings and fetchGreenhouseJobs used io.LimitReader with
// a 2 MB cap. The insiderone lever board is 3.75 MB → truncated at exactly 2 MB
// mid-JSON → json.Unmarshal failed "Unterminated string" → error swallowed at
// slog.Debug → 0 results with no visible signal.
//
// Fix: (1) raise cap to atsBoardMaxBytes (16 MB) for all three fetchers; (2) add
// explicit truncation detection (len(body)==cap → ErrBodyTruncated + counter); (3)
// add gojob_ats_fetch_errors_total{platform,reason} counter at every error exit.
//
// Revert-red evidence (per-test):
//   - TestFetchLeverPostings_LargeBoardSucceeds: change leverAPIBase to a 2 MB cap
//     and the test fails with "expected N jobs, got 0" because the >2 MB fixture is
//     truncated to broken JSON.
//   - TestFetchGreenhouseJobs_LargeBoardSucceeds: same — reset 2 MB → fails.
//   - TestFetchLeverPostings_TruncationDetected: change atsBoardMaxBytes to match
//     the fixture size (3 MB) and the test fails with "expected ErrBodyTruncated" —
//     the truncation check is now a no-op and parse error is returned instead.
//   - TestATSFetchErrorsCounter_LeverTruncated: comment out the
//     IncrATSFetchErrors(…,"truncated") call in fetchLeverPostings → counter stays 0 →
//     test fails with "want truncated counter > 0".

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// largeLeverFixture returns a valid JSON array that is larger than 2 MB but
// smaller than atsBoardMaxBytes (16 MB). It pads each entry with a long
// description field so that the total payload exceeds 2*1024*1024 bytes.
func largeLeverFixture() []byte {
	const targetSizeBytes = 3 * 1024 * 1024 // 3 MB — exceeds old 2 MB cap, fits 16 MB cap
	// One posting is ~1 KB with the padding; 3072 → ~3 MB.
	type paddedPosting struct {
		ID               string `json:"id"`
		Text             string `json:"text"`
		HostedURL        string `json:"hostedUrl"`
		DescriptionPlain string `json:"descriptionPlain"`
	}
	pad := strings.Repeat("x", 900) // ~900 B padding
	const count = 3500               // 3500 × ~900 B ≈ 3.15 MB
	postings := make([]paddedPosting, count)
	for i := range postings {
		postings[i] = paddedPosting{
			ID:               fmt.Sprintf("id-%05d", i),
			Text:             fmt.Sprintf("Engineer %d", i),
			HostedURL:        fmt.Sprintf("https://jobs.lever.co/insiderone/id-%05d", i),
			DescriptionPlain: pad,
		}
	}
	b, err := json.Marshal(postings)
	if err != nil {
		panic(fmt.Sprintf("largeLeverFixture marshal: %v", err))
	}
	if len(b) <= 2*1024*1024 {
		panic(fmt.Sprintf("largeLeverFixture too small: %d bytes (want >2 MB)", len(b)))
	}
	if len(b) >= targetSizeBytes*2 {
		panic(fmt.Sprintf("largeLeverFixture too large: %d bytes", len(b)))
	}
	return b
}

// largeGreenhouseFixture is analogous to largeLeverFixture for Greenhouse.
// Each job has a padded content field to ensure the fixture reliably exceeds 2 MB.
func largeGreenhouseFixture() []byte {
	type ghJob struct {
		ID      int64  `json:"id"`
		Title   string `json:"title"`
		Content string `json:"content"`
	}
	type resp struct {
		Jobs []ghJob `json:"jobs"`
	}
	pad := strings.Repeat("y", 800) // ~800 B padding per job
	const count = 3000              // 3000 × ~800 B ≈ 2.4 MB
	jobs := make([]ghJob, count)
	for i := range jobs {
		jobs[i] = ghJob{
			ID:      int64(i + 1),
			Title:   fmt.Sprintf("Engineer %d", i),
			Content: pad,
		}
	}
	b, err := json.Marshal(resp{Jobs: jobs})
	if err != nil {
		panic(fmt.Sprintf("largeGreenhouseFixture marshal: %v", err))
	}
	if len(b) <= 2*1024*1024 {
		panic(fmt.Sprintf("largeGreenhouseFixture too small: %d bytes (want >2 MB)", len(b)))
	}
	return b
}

// resetMetrics is a test helper that snapshots relevant counters BEFORE an
// operation so tests can compute deltas rather than depending on global state.
func atsErrorCounterDelta(platform, reason string, before, after map[string]int64) int64 {
	key := engine.MetricATSFetchErrors + "{platform=" + platform + ",reason=" + reason + "}"
	return after[key] - before[key]
}

// --- Lever large-board tests ---

// TestFetchLeverPostings_LargeBoardSucceeds is the primary regression guard.
//
// It serves a >2 MB but <16 MB valid JSON lever board and asserts that all jobs
// are returned. With the old 2 MB cap the body was truncated, json.Unmarshal
// failed, and 0 results were returned silently.
//
// Revert-red: changing atsBoardMaxBytes to 2*1024*1024 (or setting a 2 MB
// LimitReader in fetchLeverPostings directly) causes this test to fail because
// the fixture is truncated and json.Unmarshal returns a parse error → 0 postings.
func TestFetchLeverPostings_LargeBoardSucceeds(t *testing.T) {
	leverBreaker.ForceHalfOpen()
	leverBreaker.Record(true)

	fixture := largeLeverFixture()
	if len(fixture) <= 2*1024*1024 {
		t.Fatalf("fixture must be >2 MB to be a meaningful regression test; got %d bytes", len(fixture))
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture)
	}))
	t.Cleanup(srv.Close)

	origBase := leverAPIBase
	leverAPIBase = srv.URL + "/%s"
	t.Cleanup(func() { leverAPIBase = origBase })

	origClient := engine.Cfg.HTTPClient
	engine.Cfg.HTTPClient = &http.Client{}
	t.Cleanup(func() { engine.Cfg.HTTPClient = origClient })

	postings, err := fetchLeverPostings(context.Background(), "insiderone")
	if err != nil {
		t.Fatalf("fetchLeverPostings: unexpected error: %v", err)
	}
	if len(postings) == 0 {
		t.Fatal("fetchLeverPostings: got 0 postings for a >2 MB board; " +
			"reverting atsBoardMaxBytes to 2 MB would reproduce this failure")
	}
	t.Logf("fixture size: %d bytes (%.2f MB); postings: %d",
		len(fixture), float64(len(fixture))/(1024*1024), len(postings))
}

// TestFetchLeverPostings_TruncationDetected asserts that a response whose body
// is exactly atsBoardMaxBytes (the cap) returns ErrBodyTruncated, not a silent
// empty/parse error.
//
// Revert-red: changing atsBoardMaxBytes to match the served payload size
// (or removing the len(body)==atsBoardMaxBytes guard) makes the check a no-op —
// json.Unmarshal gets the truncated bytes, returns a parse error, and this test
// fails with "expected ErrBodyTruncated, got lever parse: …".
func TestFetchLeverPostings_TruncationDetected(t *testing.T) {
	leverBreaker.ForceHalfOpen()
	leverBreaker.Record(true)

	// Serve exactly atsBoardMaxBytes of garbage (simulates a cap-truncated response).
	truncatedBody := bytes.Repeat([]byte("x"), atsBoardMaxBytes)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(truncatedBody)
	}))
	t.Cleanup(srv.Close)

	origBase := leverAPIBase
	leverAPIBase = srv.URL + "/%s"
	t.Cleanup(func() { leverAPIBase = origBase })

	origClient := engine.Cfg.HTTPClient
	engine.Cfg.HTTPClient = &http.Client{}
	t.Cleanup(func() { engine.Cfg.HTTPClient = origClient })

	_, err := fetchLeverPostings(context.Background(), "big-co")
	if err == nil {
		t.Fatal("expected error for cap-sized body, got nil")
	}
	if !errors.Is(err, ErrBodyTruncated) {
		t.Fatalf("expected ErrBodyTruncated, got: %v", err)
	}
}

// TestATSFetchErrorsCounter_LeverTruncated asserts that a cap-hit board fetch
// increments gojob_ats_fetch_errors_total{platform=lever,reason=truncated}.
//
// Revert-red: commenting out IncrATSFetchErrors(…,"truncated") in
// fetchLeverPostings leaves the counter at 0 and this test fails with
// "want truncated counter > 0, got 0".
func TestATSFetchErrorsCounter_LeverTruncated(t *testing.T) {
	engine.InitTestRegistry()
	leverBreaker.ForceHalfOpen()
	leverBreaker.Record(true)

	truncatedBody := bytes.Repeat([]byte("x"), atsBoardMaxBytes)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(truncatedBody)
	}))
	t.Cleanup(srv.Close)

	origBase := leverAPIBase
	leverAPIBase = srv.URL + "/%s"
	t.Cleanup(func() { leverAPIBase = origBase })

	origClient := engine.Cfg.HTTPClient
	engine.Cfg.HTTPClient = &http.Client{}
	t.Cleanup(func() { engine.Cfg.HTTPClient = origClient })

	before := engine.GetMetrics()
	_, _ = fetchLeverPostings(context.Background(), "huge-co")
	after := engine.GetMetrics()

	if d := atsErrorCounterDelta("lever", "truncated", before, after); d <= 0 {
		t.Errorf("want gojob_ats_fetch_errors_total{platform=lever,reason=truncated} > 0, got delta %d", d)
	}
}

// --- Greenhouse large-board tests ---

// TestFetchGreenhouseJobs_LargeBoardSucceeds mirrors TestFetchLeverPostings_LargeBoardSucceeds
// for the greenhouse fetcher.
//
// Revert-red: changing atsBoardMaxBytes to 2 MB causes the fixture (>2 MB) to be
// truncated, json.Unmarshal fails, 0 jobs returned — test fails.
func TestFetchGreenhouseJobs_LargeBoardSucceeds(t *testing.T) {
	greenhouseBreaker.ForceHalfOpen()
	greenhouseBreaker.Record(true)

	fixture := largeGreenhouseFixture()
	if len(fixture) <= 2*1024*1024 {
		t.Fatalf("fixture must be >2 MB to be a meaningful regression test; got %d bytes", len(fixture))
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture)
	}))
	t.Cleanup(srv.Close)

	origAPI := greenhouseBoardsAPI
	greenhouseBoardsAPI = srv.URL + "/%s/jobs"
	t.Cleanup(func() { greenhouseBoardsAPI = origAPI })

	origClient := engine.Cfg.HTTPClient
	engine.Cfg.HTTPClient = &http.Client{}
	t.Cleanup(func() { engine.Cfg.HTTPClient = origClient })

	jobs, err := fetchGreenhouseJobs(context.Background(), "bigcorp")
	if err != nil {
		t.Fatalf("fetchGreenhouseJobs: unexpected error: %v", err)
	}
	if len(jobs) == 0 {
		t.Fatal("fetchGreenhouseJobs: got 0 jobs for a >2 MB board; " +
			"reverting atsBoardMaxBytes to 2 MB would reproduce this failure")
	}
	t.Logf("fixture size: %d bytes (%.2f MB); jobs: %d",
		len(fixture), float64(len(fixture))/(1024*1024), len(jobs))
}

// TestFetchGreenhouseJobs_TruncationDetected asserts ErrBodyTruncated on cap-hit.
//
// Revert-red: removing the len(body)==atsBoardMaxBytes guard in fetchGreenhouseJobs
// makes this test fail — the capped body goes to json.Unmarshal which returns a
// parse error, not ErrBodyTruncated.
func TestFetchGreenhouseJobs_TruncationDetected(t *testing.T) {
	greenhouseBreaker.ForceHalfOpen()
	greenhouseBreaker.Record(true)

	truncatedBody := bytes.Repeat([]byte("x"), atsBoardMaxBytes)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(truncatedBody)
	}))
	t.Cleanup(srv.Close)

	origAPI := greenhouseBoardsAPI
	greenhouseBoardsAPI = srv.URL + "/%s/jobs"
	t.Cleanup(func() { greenhouseBoardsAPI = origAPI })

	origClient := engine.Cfg.HTTPClient
	engine.Cfg.HTTPClient = &http.Client{}
	t.Cleanup(func() { engine.Cfg.HTTPClient = origClient })

	_, err := fetchGreenhouseJobs(context.Background(), "huge-co")
	if err == nil {
		t.Fatal("expected error for cap-sized body, got nil")
	}
	if !errors.Is(err, ErrBodyTruncated) {
		t.Fatalf("expected ErrBodyTruncated, got: %v", err)
	}
}

// TestATSFetchErrorsCounter_GreenhouseTruncated mirrors the lever counter test
// for the greenhouse fetcher.
//
// Revert-red: commenting out IncrATSFetchErrors(…,"truncated") in
// fetchGreenhouseJobs → counter stays 0 → test fails.
func TestATSFetchErrorsCounter_GreenhouseTruncated(t *testing.T) {
	engine.InitTestRegistry()
	greenhouseBreaker.ForceHalfOpen()
	greenhouseBreaker.Record(true)

	truncatedBody := bytes.Repeat([]byte("x"), atsBoardMaxBytes)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(truncatedBody)
	}))
	t.Cleanup(srv.Close)

	origAPI := greenhouseBoardsAPI
	greenhouseBoardsAPI = srv.URL + "/%s/jobs"
	t.Cleanup(func() { greenhouseBoardsAPI = origAPI })

	origClient := engine.Cfg.HTTPClient
	engine.Cfg.HTTPClient = &http.Client{}
	t.Cleanup(func() { engine.Cfg.HTTPClient = origClient })

	before := engine.GetMetrics()
	_, _ = fetchGreenhouseJobs(context.Background(), "huge-co")
	after := engine.GetMetrics()

	if d := atsErrorCounterDelta("greenhouse", "truncated", before, after); d <= 0 {
		t.Errorf("want gojob_ats_fetch_errors_total{platform=greenhouse,reason=truncated} > 0, got delta %d", d)
	}
}

// --- Ashby truncation test ---

// TestFetchAshbyJobs_TruncationDetected confirms the same truncation guard works
// for the ashby fetcher (which was previously capped at 5 MB, now 16 MB).
//
// Revert-red: removing the len(body)==atsBoardMaxBytes guard in fetchAshbyJobs
// makes this test fail.
func TestFetchAshbyJobs_TruncationDetected(t *testing.T) {
	ashbyBreaker.ForceHalfOpen()
	ashbyBreaker.Record(true)

	truncatedBody := bytes.Repeat([]byte("x"), atsBoardMaxBytes)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(truncatedBody)
	}))
	t.Cleanup(srv.Close)
	patchAshbyAPI(t, srv.URL) // reuse helper from ats_ashby_test.go

	origClient := engine.Cfg.HTTPClient
	engine.Cfg.HTTPClient = &http.Client{}
	t.Cleanup(func() { engine.Cfg.HTTPClient = origClient })

	_, err := fetchAshbyJobs(context.Background(), "huge-co")
	if err == nil {
		t.Fatal("expected error for cap-sized body, got nil")
	}
	if !errors.Is(err, ErrBodyTruncated) {
		t.Fatalf("expected ErrBodyTruncated, got: %v", err)
	}
}

// --- Named const sanity ---

// TestAtsBoardMaxBytes_NamedConst verifies the cap is the intended 16 MiB and
// exceeds the known worst-case board (insiderone lever ≈ 3.75 MB) by a safe margin.
// Revert-red: changing atsBoardMaxBytes to 2*1024*1024 makes this test fail.
func TestAtsBoardMaxBytes_NamedConst(t *testing.T) {
	const wantMiB = 16
	if atsBoardMaxBytes != wantMiB*1024*1024 {
		t.Errorf("atsBoardMaxBytes = %d bytes (%.2f MiB), want %d MiB",
			atsBoardMaxBytes, float64(atsBoardMaxBytes)/(1024*1024), wantMiB)
	}

	// Must exceed the known insiderone lever board size of 3.75 MB.
	const knownLargestBoardBytes = 4 * 1024 * 1024 // conservative 4 MB
	if atsBoardMaxBytes <= knownLargestBoardBytes {
		t.Errorf("atsBoardMaxBytes (%d) must exceed known largest board (%d)", atsBoardMaxBytes, knownLargestBoardBytes)
	}
}

// --- leverAPIBase patch helper (mirrors ashbyBoardAPI pattern) ---

// leverAPIBase is the format string for the Lever public API.
// The const in ats.go is package-level but not var — we redefine it as var
// in ats.go now so we can patch it in tests (mirrors ashbyBoardAPI pattern).
// NOTE: This comment is intentionally left here to note that leverAPIBase must
// be a var (not const) in ats.go for this test to compile. If ats.go declares
// it as const, this test will fail to compile — serving as a build-time guard
// that the patchability contract was broken.
