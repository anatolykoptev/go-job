package jobs

import (
	"context"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anatolykoptev/go-kit/breaker"
	"github.com/anatolykoptev/go-kit/ratelimit"
	stealth "github.com/anatolykoptev/go-stealth"
)

// withLimiter swaps the package-level linkedinLimiter and restores it. A
// high-capacity limiter guarantees Acquire never blocks on ctx, so test
// assertions about fetch counts are deterministic.
func withLimiter(t *testing.T, l *ratelimit.ConcurrencyLimiter) {
	t.Helper()
	orig := linkedinLimiter
	t.Cleanup(func() { linkedinLimiter = orig })
	linkedinLimiter = l
}

// fakeLinkedInPageHTML returns Guest-API HTML containing n parsed job cards.
// Cards carry unique job IDs so parseLinkedInHTML extracts exactly n jobs.
func fakeLinkedInPageHTML(n int) []byte {
	var sb string
	sb = "<ul>"
	for i := range n {
		id := 4000000000 + i
		sb += `<li><div class="base-card">` +
			`<a class="base-card__full-link" href="https://www.linkedin.com/jobs/view/` + itoa(id) + `">x</a>` +
			`<h3 class="base-search-card__title">Job ` + itoa(id) + `</h3>` +
			`<h4 class="base-search-card__subtitle">Acme</h4>` +
			`<div class="job-search-card__location">Remote</div>` +
			`</div></li>`
	}
	sb += "</ul>"
	return []byte(sb)
}

// itoa is a tiny strconv.Itoa to avoid pulling strconv into the test helper.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [12]byte
	pos := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// withPageJitter swaps the package-level linkedInPageJitter and restores it.
func withPageJitter(t *testing.T, j stealth.Jitter) {
	t.Helper()
	orig := linkedInPageJitter
	t.Cleanup(func() { linkedInPageJitter = orig })
	linkedInPageJitter = j
}

// startFromURL extracts the `start` query param from a URL, or -1 if absent.
func startFromURL(raw string) int {
	u, err := url.Parse(raw)
	if err != nil {
		return -1
	}
	s := u.Query().Get("start")
	if s == "" {
		return -1
	}
	v := 0
	for _, c := range s {
		v = v*10 + int(c-'0')
	}
	return v
}

// TestSearchLinkedInJobsCeilingCapsStartAt1000 asserts the guest-API hard
// ceiling (~1000 results / 40 pages) is honoured: with a fake fetch that always
// returns a full 25-result page and a maxResults well past the ceiling, the
// loop MUST NOT issue any request with start >= 1000. Pre-change the loop was
// unbounded and would paginate past 1000 (RED).
func TestSearchLinkedInJobsCeilingCapsStartAt1000(t *testing.T) {
	// Near-zero jitter so the test does not sleep between pages.
	withPageJitter(t, stealth.Jitter{Min: 0, Max: 1})
	withBreaker(t, breaker.New(breaker.Options{Name: "test", FailThreshold: 999}))
	withLimiter(t, ratelimit.NewConcurrencyLimiter(100))

	var starts []int
	var fetches atomic.Int32
	tier1 := func(_ context.Context, targetURL string, _ map[string]string) (int, []byte, error) {
		fetches.Add(1)
		starts = append(starts, startFromURL(targetURL))
		return 200, fakeLinkedInPageHTML(25), nil // liOK, full page
	}
	tierFail := func(context.Context, string, map[string]string) (int, []byte, error) {
		return 503, []byte("nope"), nil
	}
	withTiers(t, tier1, tierFail, tierFail)

	// maxResults well past the 1000 ceiling (2000 / 25 = 80 pages).
	jobs, err := SearchLinkedInJobs(context.Background(), "go", "us", "", "", "", "", "", 2000, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(jobs) == 0 {
		t.Fatal("expected non-empty result")
	}

	maxStart := 0
	for _, s := range starts {
		if s > maxStart {
			maxStart = s
		}
	}
	if maxStart >= 1000 {
		t.Errorf("max start = %d, must be < 1000 (guest-API hard ceiling); starts=%v", maxStart, starts)
	}
	// Sanity: the cap should allow the full 40 pages (0..975).
	if maxStart != 975 {
		t.Errorf("max start = %d, want 975 (40 pages × 25)", maxStart)
	}
}

// TestSearchLinkedInJobsNoBackoffBeforeFirstPage asserts (a) the first page
// fetch is immediate (no pre-fetch sleep) and (b) the second page fetch IS
// delayed by the jittered backoff. With no backoff (RED / revert), the second
// fetch would be immediate and the test fails on the delay assertion.
func TestSearchLinkedInJobsNoBackoffBeforeFirstPage(t *testing.T) {
	// Moderate jitter — first fetch must beat it, second fetch must exceed it.
	withPageJitter(t, stealth.Jitter{Min: 150 * time.Millisecond, Max: 300 * time.Millisecond})
	withBreaker(t, breaker.New(breaker.Options{Name: "test", FailThreshold: 999}))
	withLimiter(t, ratelimit.NewConcurrencyLimiter(100))

	var firstFetch, secondFetch time.Duration
	var fetches atomic.Int32
	t0 := time.Now()
	tier1 := func(_ context.Context, _ string, _ map[string]string) (int, []byte, error) {
		switch fetches.Add(1) {
		case 1:
			firstFetch = time.Since(t0)
		case 2:
			secondFetch = time.Since(t0)
		}
		return 200, fakeLinkedInPageHTML(25), nil
	}
	tierFail := func(context.Context, string, map[string]string) (int, []byte, error) {
		return 503, []byte("nope"), nil
	}
	withTiers(t, tier1, tierFail, tierFail)

	// maxResults=50 → 2 pages: first page immediate, backoff, second page.
	jobs, err := SearchLinkedInJobs(context.Background(), "go", "us", "", "", "", "", "", 50, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(jobs) != 50 {
		t.Errorf("expected 50 jobs, got %d", len(jobs))
	}
	if got := fetches.Load(); got != 2 {
		t.Fatalf("fetches = %d, want 2", got)
	}
	// (a) No backoff before the first page.
	if firstFetch > 100*time.Millisecond {
		t.Errorf("first fetch at %v, want < 100ms (no backoff before first page)", firstFetch)
	}
	// (b) Backoff fires before the second page (>= jitter.Min).
	if secondFetch < 150*time.Millisecond {
		t.Errorf("second fetch at %v, want >= 150ms (jittered backoff between pages)", secondFetch)
	}
}

// TestSearchLinkedInJobsBackoffCancellable asserts a cancelled context aborts
// pagination promptly during the inter-page backoff instead of sleeping the
// full jitter. The first page is fetched, then the context is cancelled; the
// backoff before the second page must return immediately.
func TestSearchLinkedInJobsBackoffCancellable(t *testing.T) {
	// Huge jitter — if the backoff ignored ctx.Done(), the test would hang ~5s.
	withPageJitter(t, stealth.Jitter{Min: 5 * time.Second, Max: 10 * time.Second})
	withBreaker(t, breaker.New(breaker.Options{Name: "test", FailThreshold: 999}))
	withLimiter(t, ratelimit.NewConcurrencyLimiter(100))

	ctx, cancel := context.WithCancel(context.Background())
	var fetches atomic.Int32
	tier1 := func(_ context.Context, _ string, _ map[string]string) (int, []byte, error) {
		n := fetches.Add(1)
		if n == 1 {
			// Cancel during/after the first fetch so the backoff before page 2
			// sees an already-cancelled context.
			cancel()
			return 200, fakeLinkedInPageHTML(25), nil
		}
		// Second call should never happen: backoff must abort on ctx.Done.
		return 503, []byte("should not reach"), nil
	}
	tierFail := func(context.Context, string, map[string]string) (int, []byte, error) {
		return 503, []byte("nope"), nil
	}
	withTiers(t, tier1, tierFail, tierFail)

	start := time.Now()
	// maxResults=50 → 2 pages needed, so a backoff would fire before page 2.
	jobs, err := SearchLinkedInJobs(ctx, "go", "us", "", "", "", "", "", 50, false)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(jobs) == 0 {
		t.Error("expected at least the first page of results")
	}
	if got := fetches.Load(); got != 1 {
		t.Errorf("fetches = %d, want 1 (context should abort before page 2)", got)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("elapsed = %v, want < 500ms (backoff must respect ctx.Done)", elapsed)
	}
}
