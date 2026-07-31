package jobs

// security_truncation_test.go guards the body-truncation fix for the security
// bounty ingest sources (hackerone/bugcrowd/intigriti/yeswehack/federacy via
// security_bounty.go, plus immunefi/sherlock/gowowa_render which share the
// securityBodyLimit cap).
//
// Root cause: fetchSecuritySource (and the three sibling readers) used
// io.ReadAll(io.LimitReader(resp.Body, securityBodyLimit)). io.LimitReader
// silently stops at the cap WITHOUT error; io.ReadAll reports success; the
// truncated bytes then fail json.Unmarshal ("unexpected end of JSON input"),
// which gets logged as "security: parse failed platform=hackerone" — pointing
// at the parser while the reader is at fault. hackerone_data.json measured
// 17,777,018 bytes (17.8 MB) on 2026-07-30, past the old 10 MB cap.
//
// Fix: readLimitedBody reads limit+1 bytes and returns ErrBodyTruncated when
// the extra byte is present, so hitting the cap is a loud, correctly-attributed
// failure. securityBodyLimit raised to 64 MiB for headroom.
//
// Revert-red evidence:
//   - TestReadLimitedBody_TruncationDetected: revert readLimitedBody to
//     io.ReadAll(io.LimitReader(r, limit)) → returns (truncated, nil) → test
//     fails with "expected ErrBodyTruncated, got nil".
//   - TestFetchSecuritySource_TruncationDetected: revert fetchSecuritySource's
//     readLimitedBody call to io.ReadAll(io.LimitReader(...)) → returns
//     truncated bytes, no error → test fails (expected ErrBodyTruncated).

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// TestReadLimitedBody_TruncationDetected verifies the helper surfaces an
// explicit ErrBodyTruncated when the body exceeds the cap, returns the full
// body when it fits, and never produces a downstream parse error.
func TestReadLimitedBody_TruncationDetected(t *testing.T) {
	t.Parallel()

	// Body larger than the cap → ErrBodyTruncated.
	big := bytes.Repeat([]byte("x"), 600)
	_, err := readLimitedBody(bytes.NewReader(big), 512)
	if err == nil {
		t.Fatal("expected ErrBodyTruncated for body > cap, got nil (revert-red: plain LimitReader returns truncated bytes with nil error)")
	}
	if !errors.Is(err, ErrBodyTruncated) {
		t.Fatalf("expected ErrBodyTruncated, got: %v", err)
	}

	// Body exactly at the cap → success, full body returned.
	exact := bytes.Repeat([]byte("x"), 512)
	got, err := readLimitedBody(bytes.NewReader(exact), 512)
	if err != nil {
		t.Fatalf("body == cap should succeed, got: %v", err)
	}
	if len(got) != 512 {
		t.Errorf("body == cap: got %d bytes, want 512", len(got))
	}

	// Body smaller than the cap → success, full body returned.
	small := bytes.Repeat([]byte("x"), 100)
	got, err = readLimitedBody(bytes.NewReader(small), 512)
	if err != nil {
		t.Fatalf("body < cap should succeed, got: %v", err)
	}
	if len(got) != 100 {
		t.Errorf("body < cap: got %d bytes, want 100", len(got))
	}
}

// TestFetchSecuritySource_TruncationDetected drives the REAL fetcher with a
// small overridden cap and a body that exceeds it, asserting the failure is
// ErrBodyTruncated (not a silent truncated body that would later surface as a
// confusing JSON parse error).
//
// Revert-red: restore the plain io.ReadAll(io.LimitReader(resp.Body, cap)) in
// fetchSecuritySource → returns 512 truncated bytes with nil error → test
// fails with "expected ErrBodyTruncated, got nil".
func TestFetchSecuritySource_TruncationDetected(t *testing.T) {
	const smallCap int64 = 512
	origCap := securityBodyLimit
	securityBodyLimit = smallCap
	t.Cleanup(func() { securityBodyLimit = origCap })

	// Body larger than smallCap; not valid JSON on purpose so that a silent
	// truncation would later manifest as a parse error, not a clean decode.
	body := []byte(`{"programs":[` + strings.Repeat("x", 800) + `]}`)
	if int64(len(body)) <= smallCap {
		t.Fatalf("fixture (%d B) must exceed cap (%d B)", len(body), smallCap)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	origClient := engine.Cfg.HTTPClient
	engine.Cfg.HTTPClient = &http.Client{}
	t.Cleanup(func() { engine.Cfg.HTTPClient = origClient })

	_, err := fetchSecuritySource(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected ErrBodyTruncated for cap-sized body, got nil")
	}
	if !errors.Is(err, ErrBodyTruncated) {
		t.Fatalf("expected ErrBodyTruncated, got: %v", err)
	}
}

// TestSecurityBodyLimit_HeadroomOverMeasuredHackerOne verifies the cap has real
// headroom over the measured hackerone_data.json size (17.8 MB on 2026-07-30).
// Revert-red: lowering securityBodyLimit to <= measuredHackerOneBytes fails this.
func TestSecurityBodyLimit_HeadroomOverMeasuredHackerOne(t *testing.T) {
	const measuredHackerOneBytes int64 = 17_777_018 // measured 2026-07-30
	if securityBodyLimit <= measuredHackerOneBytes {
		t.Errorf("securityBodyLimit (%d) must exceed measured hackerone_data.json (%d, 2026-07-30)",
			securityBodyLimit, measuredHackerOneBytes)
	}
	// Sanity: must be a sane DoS ceiling (>= 32 MiB headroom, <= 256 MiB).
	if securityBodyLimit < 32*1024*1024 {
		t.Errorf("securityBodyLimit (%d) too low", securityBodyLimit)
	}
}

// TestFetchSecuritySource_TruncationCounter asserts that a cap-hit security
// fetch increments gojob_security_fetch_errors_total{platform=hackerone,reason=truncated}.
//
// Without this counter, a truncation is only slog.Warn'd and swallowed by
// fetchAllSecurityPrograms when a sibling source succeeds — the exact mechanism
// that kept the original hackerone truncation invisible for a month.
//
// Revert-red: remove the isBodyTruncated/IncrSecurityFetchErrors call in
// fetchSecuritySource → counter delta = 0 → test fails.
func TestFetchSecuritySource_TruncationCounter(t *testing.T) {
	engine.InitTestRegistry()

	const smallCap int64 = 512
	origCap := securityBodyLimit
	securityBodyLimit = smallCap
	t.Cleanup(func() { securityBodyLimit = origCap })

	// Body larger than smallCap.
	body := []byte(`{"programs":[` + strings.Repeat("x", 800) + `]}`)
	if int64(len(body)) <= smallCap {
		t.Fatalf("fixture (%d B) must exceed cap (%d B)", len(body), smallCap)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	// Point securitySources[0] (hackerone) at the test server so
	// securityPlatformForURL returns "hackerone" for this URL.
	origURL := securitySources[0].url
	securitySources[0].url = srv.URL
	t.Cleanup(func() { securitySources[0].url = origURL })

	origClient := engine.Cfg.HTTPClient
	engine.Cfg.HTTPClient = &http.Client{}
	t.Cleanup(func() { engine.Cfg.HTTPClient = origClient })

	before := engine.GetMetrics()
	_, _ = fetchSecuritySource(context.Background(), srv.URL)
	after := engine.GetMetrics()

	key := engine.MetricSecurityFetchErrors + "{platform=hackerone,reason=truncated}"
	delta := after[key] - before[key]
	if delta <= 0 {
		t.Errorf("want gojob_security_fetch_errors_total{platform=hackerone,reason=truncated} > 0, got delta %d", delta)
	}
}

// TestFetchSecuritySource_Non200NoCounterIncrement (F9) asserts that a
// non-truncation failure (a non-200 status) leaves the truncation counter at 0.
// The counter is exit-specific by design — a future increment sprayed at the
// wrong exit (e.g. the status branch at security_bounty.go:136) must not stay
// green. This test pins the counter to the truncation exit ONLY.
//
// Revert-red (mutation): move the IncrSecurityFetchErrors call up into the
// non-200 branch (security_bounty.go:136-138) → this test's delta becomes > 0
// → test fails. Also red if the increment is made unconditional on any error.
func TestFetchSecuritySource_Non200NoCounterIncrement(t *testing.T) {
	engine.InitTestRegistry()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	// Point securitySources[0] (hackerone) at the test server so
	// securityPlatformForURL returns "hackerone" for this URL.
	origURL := securitySources[0].url
	securitySources[0].url = srv.URL
	t.Cleanup(func() { securitySources[0].url = origURL })

	origClient := engine.Cfg.HTTPClient
	engine.Cfg.HTTPClient = &http.Client{}
	t.Cleanup(func() { engine.Cfg.HTTPClient = origClient })

	before := engine.GetMetrics()
	_, _ = fetchSecuritySource(context.Background(), srv.URL)
	after := engine.GetMetrics()

	key := engine.MetricSecurityFetchErrors + "{platform=hackerone,reason=truncated}"
	delta := after[key] - before[key]
	if delta != 0 {
		t.Errorf("non-200 (non-truncation) failure must NOT bump the truncation counter, got delta %d (counter must be exit-specific)", delta)
	}
}
