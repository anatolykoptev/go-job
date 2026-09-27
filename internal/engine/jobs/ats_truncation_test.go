package jobs

// ats_truncation_test.go guards the body-truncation class fixed in P5 and the
// stream-decode upgrade in P6.
//
// P5 root cause: fetchLeverPostings and fetchGreenhouseJobs used io.LimitReader
// with a 2 MB cap. The insiderone lever board is 3.75 MB → truncated mid-JSON →
// json.Unmarshal failed "Unterminated string" → error swallowed at slog.Debug →
// 0 results, no signal.
//
// P5 fix: raise cap to 16 MB + explicit len(body)==cap truncation guard.
//
// P6 root cause: large sales-heavy lever boards exceed 16 MB → P5 cap fires,
// gojob_ats_fetch_errors_total{lever,reason=truncated}=1, slug never caches.
//
// P6 fix: (1) switch to stream-decode (json.NewDecoder.Decode) — no full-body
// ReadAll, incremental; (2) raise DoS ceiling to 64 MB; (3) detect cap-hit via
// countingReader (cr.n >= cap on decode error → ErrBodyTruncated); (4) genuine
// JSON parse errors → reason=parse (not truncated).
//
// Revert-red evidence (per-test):
//   - TestAtsBoardDecode_LargeBoardSucceeds (>16MB <64MB fixture): RED on old
//     16 MB cap or ReadAll+Unmarshal path — fixture exceeds cap → truncated/empty.
//     GREEN after stream-decode with 64 MB ceiling.
//   - TestAtsBoardDecode_HardCapStillFires (>64MB): always RED if cap raised or
//     removed; truncation must still fire.
//   - TestAtsBoardDecode_MalformedJSON_ReturnsParseError: RED if we return
//     ErrBodyTruncated for malformed JSON (wrong classification).
//   - TestFetchLeverPostings_LargeBoardSucceeds: change atsBoardMaxBytes or revert
//     to ReadAll with small cap → fixture truncated → 0 postings → RED.
//   - TestFetchLeverPostings_TruncationDetected: see inline comment.
//   - TestATSFetchErrorsCounter_LeverTruncated: comment out IncrATSFetchErrors →
//     counter delta = 0 → RED.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// largeLeverFixture returns a valid JSON array that is larger than 2 MB but
// smaller than 64 MB (atsBoardMaxBytes). It pads each entry with a long
// description field so that the total payload exceeds 2*1024*1024 bytes.
func largeLeverFixture() []byte {
	const targetSizeBytes = 3 * 1024 * 1024 // 3 MB — exceeds old 2 MB cap, well under 64 MB ceiling
	// One posting is ~1 KB with the padding; 3072 → ~3 MB.
	type paddedPosting struct {
		ID               string `json:"id"`
		Text             string `json:"text"`
		HostedURL        string `json:"hostedUrl"`
		DescriptionPlain string `json:"descriptionPlain"`
	}
	pad := strings.Repeat("x", 900) // ~900 B padding
	const count = 3500              // 3500 × ~900 B ≈ 3.15 MB
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

// atsErrorCounterDelta computes the delta for a labelled ATS fetch-error counter
// between two metric snapshots (before/after), so tests are independent of global
// counter state from prior test runs.
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
// hits the DoS ceiling returns ErrBodyTruncated, not a silent empty/parse error.
//
// Uses a small synthetic cap override so the test doesn't allocate 64 MB.
// The fixture is a single large JSON array (lever board format) that exceeds the cap.
//
// Revert-red: removing the cr.n >= cap guard in atsBoardDecodeWithCap makes the
// decode error return as io.ErrUnexpectedEOF/parse error instead of
// ErrBodyTruncated → test fails with "expected ErrBodyTruncated, got lever parse: …".
func TestFetchLeverPostings_TruncationDetected(t *testing.T) {
	leverBreaker.ForceHalfOpen()
	leverBreaker.Record(true)

	// Override the cap to a small value so the test doesn't send 64 MB.
	const smallCap int64 = 512
	origCap := atsBoardMaxBytes
	atsBoardMaxBytes = smallCap
	t.Cleanup(func() { atsBoardMaxBytes = origCap })

	// A lever board is a JSON array at the top level. Build one that is larger
	// than smallCap so the decoder hits the LimitReader cap mid-array.
	// Each entry is ~100 B; 10 entries = ~1 KB > 512 B cap.
	body := []byte(`[{"id":"aa","text":"Eng","hostedUrl":"https://jobs.lever.co/acme/aa","descriptionPlain":"` +
		strings.Repeat("x", 500) + `"},{"id":"bb","text":"Eng2","hostedUrl":"https://jobs.lever.co/acme/bb","descriptionPlain":"` +
		strings.Repeat("y", 500) + `"}]`)
	if int64(len(body)) <= smallCap {
		t.Fatalf("fixture (%d B) must exceed cap (%d B)", len(body), smallCap)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
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
// Uses a small cap override so the test doesn't allocate 64 MB.
//
// Revert-red: commenting out IncrATSFetchErrors(…,"truncated") in
// fetchLeverPostings leaves the counter at 0 and this test fails with
// "want truncated counter > 0, got 0".
func TestATSFetchErrorsCounter_LeverTruncated(t *testing.T) {
	engine.InitTestRegistry()
	leverBreaker.ForceHalfOpen()
	leverBreaker.Record(true)

	const smallCap int64 = 512
	origCap := atsBoardMaxBytes
	atsBoardMaxBytes = smallCap
	t.Cleanup(func() { atsBoardMaxBytes = origCap })

	// Large lever board (array) that exceeds the small cap.
	body := []byte(`[{"id":"aa","text":"Eng","hostedUrl":"https://jobs.lever.co/acme/aa","descriptionPlain":"` +
		strings.Repeat("x", 500) + `"},{"id":"bb","text":"Eng2","hostedUrl":"https://jobs.lever.co/acme/bb","descriptionPlain":"` +
		strings.Repeat("y", 500) + `"}]`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
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
// Uses a small cap override so the test doesn't allocate 64 MB.
// The fixture is a single `{"jobs":[...]}` blob (greenhouse board format) that
// exceeds the cap — the decoder reads it as one continuous stream.
//
// Revert-red: removing the cr.n >= cap guard in atsBoardDecodeWithCap → decode
// error returns as parse error, not ErrBodyTruncated.
func TestFetchGreenhouseJobs_TruncationDetected(t *testing.T) {
	greenhouseBreaker.ForceHalfOpen()
	greenhouseBreaker.Record(true)

	const smallCap int64 = 512
	origCap := atsBoardMaxBytes
	atsBoardMaxBytes = smallCap
	t.Cleanup(func() { atsBoardMaxBytes = origCap })

	// Greenhouse board is a JSON object {"jobs":[...]} — must be a single large object
	// so the decoder reads past the cap while parsing the jobs array.
	body := []byte(`{"jobs":[{"id":1,"title":"` +
		strings.Repeat("x", 600) + `"},{"id":2,"title":"` +
		strings.Repeat("y", 600) + `"}]}`)
	if int64(len(body)) <= smallCap {
		t.Fatalf("fixture (%d B) must exceed cap (%d B)", len(body), smallCap)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
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
// Uses a small cap override so the test doesn't allocate 64 MB.
//
// Revert-red: commenting out IncrATSFetchErrors(…,"truncated") in
// fetchGreenhouseJobs → counter stays 0 → test fails.
func TestATSFetchErrorsCounter_GreenhouseTruncated(t *testing.T) {
	engine.InitTestRegistry()
	greenhouseBreaker.ForceHalfOpen()
	greenhouseBreaker.Record(true)

	const smallCap int64 = 512
	origCap := atsBoardMaxBytes
	atsBoardMaxBytes = smallCap
	t.Cleanup(func() { atsBoardMaxBytes = origCap })

	// Single greenhouse-format object that exceeds the cap.
	body := []byte(`{"jobs":[{"id":1,"title":"` +
		strings.Repeat("x", 600) + `"},{"id":2,"title":"` +
		strings.Repeat("y", 600) + `"}]}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
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

// TestFetchAshbyJobs_TruncationDetected confirms the truncation guard works
// for the ashby fetcher.
//
// Uses a small cap override so the test doesn't allocate 64 MB.
// The fixture is a single `{"jobs":[...]}` blob (ashby board format) that exceeds
// the cap — the decoder reads it as one continuous stream.
//
// Revert-red: removing the cr.n >= cap guard in atsBoardDecodeWithCap → decode
// error returns as parse error, not ErrBodyTruncated.
func TestFetchAshbyJobs_TruncationDetected(t *testing.T) {
	ashbyBreaker.ForceHalfOpen()
	ashbyBreaker.Record(true)

	const smallCap int64 = 512
	origCap := atsBoardMaxBytes
	atsBoardMaxBytes = smallCap
	t.Cleanup(func() { atsBoardMaxBytes = origCap })

	// Ashby board is {"jobs":[...]} — a single object whose jobs array makes
	// the total body exceed the cap, forcing the decoder past the LimitReader ceiling.
	body := []byte(`{"jobs":[{"id":"aa","title":"` +
		strings.Repeat("x", 600) + `"},{"id":"bb","title":"` +
		strings.Repeat("y", 600) + `"}]}`)
	if int64(len(body)) <= smallCap {
		t.Fatalf("fixture (%d B) must exceed cap (%d B)", len(body), smallCap)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
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

// TestAtsBoardMaxBytes_NamedConst verifies the DoS ceiling is the intended 64 MiB
// and exceeds the known worst-case boards (insiderone lever ≈ 3.75 MB, plus
// sales-heavy lever boards confirmed >16 MB per P6 incident).
// Revert-red: changing atsBoardMaxBytes to 16*1024*1024 makes this test fail.
func TestAtsBoardMaxBytes_NamedConst(t *testing.T) {
	const wantMiB = 64
	if atsBoardMaxBytes != wantMiB*1024*1024 {
		t.Errorf("atsBoardMaxBytes = %d bytes (%.2f MiB), want %d MiB",
			atsBoardMaxBytes, float64(atsBoardMaxBytes)/(1024*1024), wantMiB)
	}

	// Must exceed the P6 confirmed sales-heavy lever board (>16 MB).
	const p6LargestBoardBytes = 16 * 1024 * 1024
	if atsBoardMaxBytes <= p6LargestBoardBytes {
		t.Errorf("atsBoardMaxBytes (%d) must exceed P6 sales-heavy board size (%d)",
			atsBoardMaxBytes, p6LargestBoardBytes)
	}
}

// --- atsBoardDecodeWithCap unit tests (no HTTP, small caps, fast) ---

// TestAtsBoardDecode_LargeBoardSucceeds is the primary P6 regression guard.
//
// It decodes a >16 MB but <64 MB valid JSON fixture via atsBoardDecodeWithCap
// with the new 64 MB ceiling. Proves the P5-era 16 MB cap was the bottleneck:
//
// Revert-red: call atsBoardDecodeWithCap with cap=16*1024*1024 — the 20 MB
// fixture hits the cap mid-decode → ErrBodyTruncated → test fails with
// "expected N jobs, got error: source: ATS board body truncated at read cap".
func TestAtsBoardDecode_LargeBoardSucceeds(t *testing.T) {
	// Build a >16 MB valid JSON array by repeating a padded entry.
	type entry struct {
		ID   string `json:"id"`
		Text string `json:"text"`
		Desc string `json:"desc"`
	}
	pad := strings.Repeat("a", 1000)
	const count = 20000 // 20000 × ~1KB ≈ 20 MB
	entries := make([]entry, count)
	for i := range entries {
		entries[i] = entry{
			ID:   fmt.Sprintf("id-%06d", i),
			Text: fmt.Sprintf("Job %d", i),
			Desc: pad,
		}
	}
	fixture, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if len(fixture) <= 16*1024*1024 {
		t.Fatalf("fixture must be >16 MB; got %d bytes", len(fixture))
	}
	t.Logf("fixture size: %.2f MB", float64(len(fixture))/(1024*1024))

	var got []entry
	if err := atsBoardDecodeWithCap(bytes.NewReader(fixture), atsBoardMaxBytes, &got); err != nil {
		t.Fatalf("atsBoardDecodeWithCap: unexpected error: %v (revert-red: use 16 MB cap)", err)
	}
	if len(got) != count {
		t.Errorf("got %d entries, want %d", len(got), count)
	}
}

// TestAtsBoardDecode_HardCapStillFires verifies that a body exceeding the full
// 64 MB ceiling still returns ErrBodyTruncated (DoS guard preserved).
//
// Uses a small synthetic cap so the test does not allocate 64 MB.
//
// Revert-red: remove the cr.n >= cap guard in atsBoardDecodeWithCap — the decode
// error is returned as-is (io.EOF or unexpected-EOF), not ErrBodyTruncated →
// reason=parse instead of reason=truncated → test fails.
func TestAtsBoardDecode_HardCapStillFires(t *testing.T) {
	const smallCap = 100 // synthetic tiny cap — same code path as 64 MB ceiling

	// A valid JSON object that is larger than smallCap.
	payload := []byte(`{"jobs":[` + strings.Repeat(`{"id":"x","title":"y"},`, 20) + `{}]}`)
	if int64(len(payload)) <= smallCap {
		t.Fatalf("payload (%d B) must exceed cap (%d B)", len(payload), smallCap)
	}

	var target map[string]any
	err := atsBoardDecodeWithCap(bytes.NewReader(payload), smallCap, &target)
	if err == nil {
		t.Fatal("expected ErrBodyTruncated, got nil")
	}
	if !errors.Is(err, ErrBodyTruncated) {
		t.Fatalf("expected ErrBodyTruncated, got: %v", err)
	}
}

// TestAtsBoardDecode_MalformedJSON_ReturnsParseError verifies that a genuine JSON
// parse error (not a cap hit) returns the raw decode error, NOT ErrBodyTruncated.
//
// Revert-red: change the cr.n >= cap branch to always return ErrBodyTruncated —
// this test fails with "expected non-ErrBodyTruncated error, got ErrBodyTruncated".
func TestAtsBoardDecode_MalformedJSON_ReturnsParseError(t *testing.T) {
	malformed := []byte(`{"jobs": [not valid json]}`)
	const largeCap = 1024 * 1024 // 1 MB — well above the malformed payload

	var target map[string]any
	err := atsBoardDecodeWithCap(bytes.NewReader(malformed), largeCap, &target)
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
	if errors.Is(err, ErrBodyTruncated) {
		t.Fatalf("malformed JSON must return a parse error, not ErrBodyTruncated; got: %v", err)
	}
	// Must not be EOF — it is a real parse error.
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("expected syntax error, not EOF variant: %v", err)
	}
}
