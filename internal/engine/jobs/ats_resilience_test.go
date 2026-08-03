package jobs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anatolykoptev/go-kit/breaker"
	"github.com/anatolykoptev/go-kit/ratelimit"
)

// --- Change B: ConcurrencyLimiter ---

// TestConcurrencyLimiter_AcquireReleasePairing verifies that Acquire returns a
// release func and that released slots become available again. The prod pattern
// in fetchAshbyJobs uses `release, err := atsLimiter.Acquire(ctx); defer release()`.
func TestConcurrencyLimiter_AcquireReleasePairing(t *testing.T) {
	lim := ratelimit.NewConcurrencyLimiter(2)

	ctx := context.Background()
	rel1, err := lim.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire 1 failed: %v", err)
	}
	rel2, err := lim.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire 2 failed: %v", err)
	}

	// Both slots held — available should be 0.
	if got := lim.Available(); got != 0 {
		t.Fatalf("available = %d, want 0", got)
	}

	// TryAcquire should fail while at capacity.
	if _, ok := lim.TryAcquire(); ok {
		t.Fatal("TryAcquire succeeded while at capacity")
	}

	rel1()
	if got := lim.Available(); got != 1 {
		t.Fatalf("available after release = %d, want 1", got)
	}

	rel2()
	if got := lim.Available(); got != 2 {
		t.Fatalf("available after both releases = %d, want 2", got)
	}

	// Idempotent: double-release must be safe.
	rel1()
	rel2()
	if got := lim.Available(); got != 2 {
		t.Fatalf("available after idempotent double-release = %d, want 2", got)
	}
}

// TestAshbyFetcher_ConcurrencyLimited verifies that the N+1th goroutine blocks
// until one of the N running goroutines releases. The test uses atsLimiter
// directly (the prod var) and an httptest server that blocks until told.
func TestAshbyFetcher_ConcurrencyLimited(t *testing.T) {
	origLimiter := atsLimiter
	origBreaker := ashbyBreaker
	t.Cleanup(func() {
		atsLimiter = origLimiter
		ashbyBreaker = origBreaker
	})

	limit := 2
	testLimiter := ratelimit.NewConcurrencyLimiter(limit)
	atsLimiter = testLimiter

	// Breaker that always allows (closed state).
	testBreaker := breaker.New(breaker.Options{FailThreshold: 100, OpenDuration: time.Second})
	ashbyBreaker = testBreaker

	blocker := make(chan struct{})
	var activeCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		activeCount.Add(1)
		defer activeCount.Add(-1)
		<-blocker // block until test unblocks
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jobs":[]}`)
	}))
	defer srv.Close()

	// Point ashbyBoardAPI at the test server.
	origAPI := ashbyBoardAPI
	ashbyBoardAPI = srv.URL + "/%s"
	defer func() { ashbyBoardAPI = origAPI }()

	ctx := context.Background()
	var wg sync.WaitGroup

	// Launch `limit` goroutines — they will all block at the server handler.
	for i := range limit {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = fetchAshbyJobs(ctx, fmt.Sprintf("slug%d", i))
		}(i)
	}

	// Wait until all `limit` goroutines are active in the server.
	deadline := time.Now().Add(2 * time.Second)
	for activeCount.Load() < int32(limit) {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %d active handlers, got %d", limit, activeCount.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}

	// N+1th goroutine: should block on limiter, not reach server.
	extra := make(chan error, 1)
	go func() {
		ctxTimeout, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		defer cancel()
		_, err := fetchAshbyJobs(ctxTimeout, "slug-extra")
		extra <- err
	}()

	// Give extra time to attempt — should not get through (server still at capacity).
	time.Sleep(50 * time.Millisecond)
	if activeCount.Load() > int32(limit) {
		t.Fatalf("extra goroutine bypassed limiter — active = %d, want <= %d", activeCount.Load(), limit)
	}

	// Unblock all server handlers — releases limiter slots.
	close(blocker)

	// extra goroutine should now error (ctx timeout, which proves it was blocked).
	if err := <-extra; err == nil {
		// If it succeeded (because a slot freed up in time), that's also valid.
		// The invariant is that it was not active while limit was held.
		t.Log("extra goroutine succeeded after slot freed — limiter worked correctly")
	}

	wg.Wait()
}

// --- Change C: Circuit Breaker ---

// TestAshbyFetcher_BreakerOpensAfterFails drives the prod fetchAshbyJobs function
// through FailThreshold failures and asserts the next call returns ErrOpen without
// an HTTP attempt.
func TestAshbyFetcher_BreakerOpensAfterFails(t *testing.T) {
	origBreaker := ashbyBreaker
	origLimiter := atsLimiter
	t.Cleanup(func() {
		ashbyBreaker = origBreaker
		atsLimiter = origLimiter
	})

	failThreshold := uint32(3)
	testBreaker := breaker.New(breaker.Options{
		Name:          "ashby-test",
		FailThreshold: failThreshold,
		OpenDuration:  5 * time.Minute, // long — won't auto-half-open during test
	})
	ashbyBreaker = testBreaker

	// Unlimited concurrency so limiter never blocks.
	atsLimiter = ratelimit.NewConcurrencyLimiter(10)

	var httpCallCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		httpCallCount.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	origAPI := ashbyBoardAPI
	ashbyBoardAPI = srv.URL + "/%s"
	defer func() { ashbyBoardAPI = origAPI }()

	ctx := context.Background()

	// Drive FailThreshold failures — breaker should trip after last one.
	for i := range failThreshold {
		_, err := fetchAshbyJobs(ctx, "slugX")
		if err == nil {
			t.Fatalf("call %d: expected error from 500 response, got nil", i+1)
		}
		if errors.Is(err, breaker.ErrOpen) {
			t.Fatalf("call %d: breaker opened too early (at %d/%d failures)", i+1, i+1, failThreshold)
		}
	}

	httpBefore := httpCallCount.Load()

	// Next call must be blocked by the open breaker — no HTTP attempt.
	_, err := fetchAshbyJobs(ctx, "slugX")
	if !errors.Is(err, breaker.ErrOpen) {
		t.Fatalf("expected breaker.ErrOpen, got: %v", err)
	}

	if httpCallCount.Load() != httpBefore {
		t.Fatalf("HTTP was called after breaker opened (count went %d → %d)", httpBefore, httpCallCount.Load())
	}
}

// TestAshbyFetcher_BreakerHalfOpensAfterDuration verifies that after OpenDuration
// elapses, the breaker transitions to half-open and allows the next probe attempt.
func TestAshbyFetcher_BreakerHalfOpensAfterDuration(t *testing.T) {
	origBreaker := ashbyBreaker
	origLimiter := atsLimiter
	t.Cleanup(func() {
		ashbyBreaker = origBreaker
		atsLimiter = origLimiter
	})

	testBreaker := breaker.New(breaker.Options{
		Name:          "ashby-halfopen-test",
		FailThreshold: 1,
		OpenDuration:  50 * time.Millisecond, // short for test speed
	})
	ashbyBreaker = testBreaker
	atsLimiter = ratelimit.NewConcurrencyLimiter(10)

	var httpCallCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		httpCallCount.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	origAPI := ashbyBoardAPI
	ashbyBoardAPI = srv.URL + "/%s"
	defer func() { ashbyBoardAPI = origAPI }()

	ctx := context.Background()

	// Trip the breaker.
	_, err := fetchAshbyJobs(ctx, "slug1")
	if err == nil {
		t.Fatal("expected error from 500 response")
	}
	if errors.Is(err, breaker.ErrOpen) {
		t.Fatal("breaker opened on first call unexpectedly")
	}

	// Breaker is now open — next call returns ErrOpen immediately.
	_, err = fetchAshbyJobs(ctx, "slug1")
	if !errors.Is(err, breaker.ErrOpen) {
		t.Fatalf("expected ErrOpen while breaker is open, got: %v", err)
	}

	// Wait for OpenDuration to elapse → breaker transitions to half-open.
	time.Sleep(100 * time.Millisecond)

	beforeCount := httpCallCount.Load()

	// Half-open probe: should attempt HTTP (one call allowed through).
	_, _ = fetchAshbyJobs(ctx, "slug1")

	if httpCallCount.Load() <= beforeCount {
		t.Fatal("half-open probe did not reach HTTP server — breaker did not transition to half-open")
	}
}

// TestAshbyFetcher_LogsOriginalErrorBeforeBreakerTrip verifies that when the
// fetcher fails (HTTP 500), the original error is logged with slog.Warn
// inside the defer before breaker.Record. Without this, the caller only sees
// the breaker-open error after the breaker trips — the original HTTP status
// that CAUSED the trip is lost (#464).
//
// The log line must contain the "pre-breaker" marker, the slug, and the
// original error text (HTTP 500 status), distinguishable from the caller's
// "fetch error" log which only sees the wrapped breaker.ErrOpen.
//
// Deliberately NOT t.Parallel(): this test swaps the process-global slog
// default to capture output into a local bytes.Buffer. Running it in the
// parallel phase lets a sibling test's slog.Warn write into that buffer
// while buf.String() reads it — bytes.Buffer is not concurrency-safe.
func TestAshbyFetcher_LogsOriginalErrorBeforeBreakerTrip(t *testing.T) {
	origBreaker := ashbyBreaker
	origLimiter := atsLimiter
	t.Cleanup(func() {
		ashbyBreaker = origBreaker
		atsLimiter = origLimiter
	})

	testBreaker := breaker.New(breaker.Options{
		Name:          "ashby-log-test",
		FailThreshold: 3,
		OpenDuration:  5 * time.Minute,
	})
	ashbyBreaker = testBreaker
	atsLimiter = ratelimit.NewConcurrencyLimiter(10)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	origAPI := ashbyBoardAPI
	ashbyBoardAPI = srv.URL + "/%s"
	defer func() { ashbyBoardAPI = origAPI }()

	var buf bytes.Buffer
	handler := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(oldLogger) })

	ctx := context.Background()
	_, err := fetchAshbyJobs(ctx, "slugLogTest")
	if err == nil {
		t.Fatal("expected error from 500 response")
	}
	if errors.Is(err, breaker.ErrOpen) {
		t.Fatal("breaker opened on first call unexpectedly")
	}

	out := buf.String()
	// Anchored regexp: the WARN line must contain the "pre-breaker" marker
	// and the slug. A bare Contains("level=WARN") could be satisfied by a
	// foreign test's log writing into the process-global slog sink.
	re := regexp.MustCompile(`level=WARN[^\n]*ashby: fetch failed \(pre-breaker\)[^\n]*slugLogTest`)
	if !re.MatchString(out) {
		t.Errorf("expected a WARN log line matching %q, got:\n%s", re.String(), out)
	}
}
