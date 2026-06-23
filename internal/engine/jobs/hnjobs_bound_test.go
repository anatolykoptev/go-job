package jobs

// hnjobs_bound_test.go is the regression guard for the HN fan-out hang
// (2026-06-23 discovery-collapse arc, H2). FetchHNJobComments fanned out 80+
// Firebase fetches with retry/backoff and a collector loop that blocked on the
// result channel with NO overall deadline and NO escape — a slow/throttling
// Firebase could hang the call ~10 minutes, far past the 90s MCP deadline.
//
// The real bug is the no-parent-deadline path: a caller passing a bare context
// (no timeout) had NOTHING bounding the fan-out. The fix bounds it with
// hnFanoutBudget and joins all workers before returning so none outlive the
// call (no goroutine leak, no data race on shared engine.Cfg).
//
// Two cases:
//   1. TestFetchHNJobComments_BudgetEscape_NoParentDeadline — the load-bearing
//      one: parent ctx has NO deadline, only hnFanoutBudget bounds it. Asserts
//      the call returns at ~budget. This is RED against the original (no
//      overall-budget) code and would NOT be caught by a cancellable-parent
//      test (the pre-fix code already honored parent cancellation).
//   2. TestFetchHNJobComments_ReturnsOnParentCancel — parent cancellation also
//      unblocks promptly (covers the inherited-cancel path).

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// hnBoundTestThreadID is the thread the stub answers with a populated kids list
// so FetchHNJobComments reaches the fan-out collector (the actual fix site).
const hnBoundTestThreadID = 12345678

// fanoutStallRoundTripper answers the thread request immediately (so the fan-out
// starts) and stalls every kid request until its context is cancelled —
// simulating a Firebase that responds to the thread but hangs on per-comment
// fetches. It counts in-flight kid stalls so a test can assert the join drained
// them (no orphaned goroutines outliving the call).
type fanoutStallRoundTripper struct {
	inflight atomic.Int64
}

func (rt *fanoutStallRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	threadPath := fmt.Sprintf("/item/%d.json", hnBoundTestThreadID)
	if strings.HasSuffix(req.URL.Path, threadPath) {
		var sb strings.Builder
		sb.WriteString(`{"id":12345678,"type":"story","kids":[`)
		for i := 0; i < 200; i++ {
			if i > 0 {
				sb.WriteByte(',')
			}
			fmt.Fprintf(&sb, "%d", 90000000+i)
		}
		sb.WriteString(`]}`)
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(sb.String())),
			Header:     make(http.Header),
		}, nil
	}
	// Kid request: stall until cancelled (budget expiry or join cancel).
	rt.inflight.Add(1)
	defer rt.inflight.Add(-1)
	<-req.Context().Done()
	return nil, req.Context().Err()
}

// withStallingHN swaps engine.Cfg.HTTPClient/FetchTimeout for the duration of
// fn and restores them after. Safe under -race because FetchHNJobComments joins
// all workers before returning, so no goroutine reads engine.Cfg after fn ends.
func withStallingHN(t *testing.T, fn func(rt *fanoutStallRoundTripper)) {
	t.Helper()
	prevClient := engine.Cfg.HTTPClient
	prevTimeout := engine.Cfg.FetchTimeout
	defer func() {
		engine.Cfg.HTTPClient = prevClient
		engine.Cfg.FetchTimeout = prevTimeout
	}()

	rt := &fanoutStallRoundTripper{}
	engine.Cfg.HTTPClient = &http.Client{Transport: rt}
	// Short per-call timeout so the thread fetch's retries (if any) are quick;
	// kid fetches stall regardless and are bounded by the fan-out budget.
	engine.Cfg.FetchTimeout = 50 * time.Millisecond
	fn(rt)
}

// TestFetchHNJobComments_BudgetEscape_NoParentDeadline is the load-bearing
// regression test: with a parent ctx that has NO deadline, only hnFanoutBudget
// bounds the fan-out. The call MUST return at ~budget — not hang. This is the
// real 10-minute prod-hang path, and it is RED on the pre-fix code (which had
// no overall budget and would block on every stalled kid).
func TestFetchHNJobComments_BudgetEscape_NoParentDeadline(t *testing.T) {
	// Shrink the budget so the escape happens in ms, not 45s. Restore after.
	prevBudget := hnFanoutBudget
	hnFanoutBudget = 300 * time.Millisecond
	defer func() { hnFanoutBudget = prevBudget }()

	withStallingHN(t, func(rt *fanoutStallRoundTripper) {
		// Bare parent context — NO deadline. Only hnFanoutBudget can bound this.
		ctx := context.Background()

		var elapsed time.Duration
		done := make(chan struct{})
		start := time.Now()
		go func() {
			_, _ = FetchHNJobComments(ctx, hnBoundTestThreadID, 20)
			elapsed = time.Since(start)
			close(done)
		}()

		select {
		case <-done:
			// Must have returned at roughly the budget, not instantly (instant
			// would mean the fan-out never ran) and not hung.
			if elapsed < 200*time.Millisecond {
				t.Fatalf("returned too fast (%s) — fan-out did not actually run "+
					"to the budget escape", elapsed)
			}
			if elapsed > 5*time.Second {
				t.Fatalf("returned after %s — far past the %s budget; the "+
					"budget escape is not bounding the no-parent-deadline path",
					elapsed, hnFanoutBudget)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("FetchHNJobComments did NOT return under a bare (no-deadline) " +
				"parent ctx within 10s — the no-overall-budget hang regressed " +
				"(this is the real ~10-min prod hang)")
		}

		// After return, the join must have drained every stalled kid worker —
		// no goroutine outlives the call. Allow a brief settle for atomic decrement.
		deadline := time.Now().Add(2 * time.Second)
		for rt.inflight.Load() != 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if n := rt.inflight.Load(); n != 0 {
			t.Fatalf("%d kid fetches still in flight after FetchHNJobComments "+
				"returned — workers leaked past the join (BLOCKER 2)", n)
		}
	})
}

// TestFetchHNJobComments_ReturnsOnParentCancel covers the inherited-cancel path:
// a parent ctx that is cancelled before the budget must also unblock promptly.
func TestFetchHNJobComments_ReturnsOnParentCancel(t *testing.T) {
	withStallingHN(t, func(_ *fanoutStallRoundTripper) {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()

		done := make(chan struct{})
		go func() {
			_, _ = FetchHNJobComments(ctx, hnBoundTestThreadID, 20)
			close(done)
		}()

		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("FetchHNJobComments did not return after parent cancellation")
		}
	})
}

// TestConcurrentFetchHNDoesNotLeak runs several FetchHNJobComments calls
// concurrently against the stalling stub with a short budget and asserts they
// all return and leave zero in-flight workers — the join must prevent the
// "fetch × concurrent-calls orphaned goroutines" accumulation the reviewer flagged.
func TestConcurrentFetchHNDoesNotLeak(t *testing.T) {
	prevBudget := hnFanoutBudget
	hnFanoutBudget = 200 * time.Millisecond
	defer func() { hnFanoutBudget = prevBudget }()

	withStallingHN(t, func(rt *fanoutStallRoundTripper) {
		const callers = 4
		var wg sync.WaitGroup
		wg.Add(callers)
		for i := 0; i < callers; i++ {
			go func() {
				defer wg.Done()
				_, _ = FetchHNJobComments(context.Background(), hnBoundTestThreadID, 20)
			}()
		}

		joined := make(chan struct{})
		go func() { wg.Wait(); close(joined) }()
		select {
		case <-joined:
		case <-time.After(15 * time.Second):
			t.Fatal("concurrent FetchHNJobComments calls did not all return — " +
				"fan-out join regressed under concurrency")
		}

		deadline := time.Now().Add(2 * time.Second)
		for rt.inflight.Load() != 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if n := rt.inflight.Load(); n != 0 {
			t.Fatalf("%d kid fetches still in flight after %d concurrent callers "+
				"returned — orphaned goroutines accumulated", n, callers)
		}
	})
}

// TestHNFanoutBudgetWithinDeadline locks the budget below the 90s MCP deadline
// with headroom for thread-find + Algolia + LLM post-processing. If a future
// edit bumps it past the deadline, the hang class can recur silently.
func TestHNFanoutBudgetWithinDeadline(t *testing.T) {
	if hnFanoutBudget >= hnFanoutBudgetMax {
		t.Fatalf("hnFanoutBudget (%s) must stay below the MCP deadline ceiling "+
			"(%s) with headroom for the rest of the HN search path",
			hnFanoutBudget, hnFanoutBudgetMax)
	}
}
