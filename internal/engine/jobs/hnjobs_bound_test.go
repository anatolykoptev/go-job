package jobs

// hnjobs_bound_test.go is the regression guard for the HN fan-out hang
// (2026-06-23 discovery-collapse arc, H2). FetchHNJobComments fanned out 80+
// Firebase fetches with retry/backoff and a collector loop that blocked on the
// result channel with NO overall deadline and NO ctx.Done() escape — a slow or
// throttling Firebase could hang the call ~10 minutes, far past the 90s MCP
// deadline. The fix bounds the fan-out with hnFanoutBudget and makes the
// collector return partial results on context cancellation.
//
// This test installs a RoundTripper that blocks every request until its
// context is cancelled, then proves FetchHNJobComments returns PROMPTLY when
// the parent context is cancelled — rather than blocking on every stalled
// goroutine. It deliberately uses a short parent timeout (cancellation
// inherits into fanoutCtx) so the assertion runs in milliseconds, not 45s.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// threadID used by the bound test; the thread fetch returns a populated kids
// list so the fan-out collector path (the actual fix site) is exercised.
const hnBoundTestThreadID = 12345678

// fanoutStallRoundTripper returns a valid thread item (with many kids) for the
// thread request so FetchHNJobComments reaches the fan-out, then blocks every
// kid request until its context is cancelled — simulating a Firebase that
// answers the thread but stalls on the per-comment fetches.
type fanoutStallRoundTripper struct{}

func (fanoutStallRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	threadPath := fmt.Sprintf("/item/%d.json", hnBoundTestThreadID)
	if strings.HasSuffix(req.URL.Path, threadPath) {
		// Synthesize a thread with 200 kids — enough to drive the fan-out.
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
	// Every kid request stalls until cancelled.
	<-req.Context().Done()
	return nil, req.Context().Err()
}

func TestFetchHNJobComments_ReturnsOnContextCancel(t *testing.T) {
	// Save and restore the shared engine config touched by this test.
	prevClient := engine.Cfg.HTTPClient
	prevTimeout := engine.Cfg.FetchTimeout
	t.Cleanup(func() {
		engine.Cfg.HTTPClient = prevClient
		engine.Cfg.FetchTimeout = prevTimeout
	})

	engine.Cfg.HTTPClient = &http.Client{Transport: fanoutStallRoundTripper{}}
	engine.Cfg.FetchTimeout = 50 * time.Millisecond

	// Parent ctx cancels well before hnFanoutBudget (45s). Because fanoutCtx is
	// derived from this ctx, cancellation propagates and the collector must
	// return promptly instead of blocking on every stalled goroutine.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		// The thread fetch succeeds (kids populated) so the fan-out collector
		// path runs; every kid stalls. We assert ONLY that the call returns and
		// does not hang — the collector must honor fanoutCtx cancellation.
		_, _ = FetchHNJobComments(ctx, hnBoundTestThreadID, 20)
		close(done)
	}()

	select {
	case <-done:
		// Returned within the parent budget — bound holds.
	case <-time.After(10 * time.Second):
		t.Fatal("FetchHNJobComments did not return within 10s under a stalling " +
			"Firebase — the fan-out hang regressed (no overall budget / no " +
			"ctx.Done() escape in the collector)")
	}
}

// TestHNFanoutBudgetWithinDeadline locks the budget constant below the 90s MCP
// deadline with headroom for thread-find + Algolia + LLM post-processing. If a
// future edit bumps it past the deadline, the hang class can recur silently.
func TestHNFanoutBudgetWithinDeadline(t *testing.T) {
	const mcpDeadline = 90 * time.Second
	if hnFanoutBudget >= mcpDeadline {
		t.Fatalf("hnFanoutBudget (%s) must stay below the MCP deadline (%s) "+
			"with headroom for the rest of the HN search path", hnFanoutBudget, mcpDeadline)
	}
}
