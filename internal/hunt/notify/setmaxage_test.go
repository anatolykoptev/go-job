package notify_test

import (
	"testing"
	"time"

	kitnotify "github.com/anatolykoptev/go-kit/telegram/notify"
	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/anatolykoptev/go_job/internal/hunt/notify"
	"github.com/stretchr/testify/assert"
)

// TestSetMaxAge_UpdatesRecencyGate verifies that SetMaxAge updates the
// recency gate at runtime, so a job that was fresh under the old gate
// becomes stale under the new tighter gate.
func TestSetMaxAge_UpdatesRecencyGate(t *testing.T) {
	stub := &stubSender{}
	sink := kitnotify.NewProductSink(stub, kitnotify.WithRPS(1000))
	n := notify.NewFromSinkWithMaxAge(sink, 48*time.Hour, testChatID)

	// Job posted 10h ago — fresh under 48h gate.
	postedAt := time.Now().Add(-10 * time.Hour)
	n.NotifyNewJob(hunt.Job{Title: "fresh-under-48h", URL: "https://jobs.io/1", Source: "greenhouse", PostedAt: &postedAt}, nil)
	drainDispatch()
	assert.Equal(t, int32(1), stub.callCount.Load(), "10h-old job sends under 48h gate")

	// Tighten the gate to 5h at runtime (simulates admin-UI change).
	n.SetMaxAge(5 * time.Hour)

	// Same 10h-old job is now stale under the 5h gate.
	stub.callCount.Store(0)
	n.NotifyNewJob(hunt.Job{Title: "stale-under-5h", URL: "https://jobs.io/2", Source: "greenhouse", PostedAt: &postedAt}, nil)
	drainDispatch()
	assert.Equal(t, int32(0), stub.callCount.Load(), "10h-old job skipped under 5h gate after SetMaxAge")
}

// TestSetMaxAge_ZeroResetsToDefault verifies that SetMaxAge(0) resets to
// the default 48h.
func TestSetMaxAge_ZeroResetsToDefault(t *testing.T) {
	stub := &stubSender{}
	sink := kitnotify.NewProductSink(stub, kitnotify.WithRPS(1000))
	n := notify.NewFromSinkWithMaxAge(sink, 1*time.Hour, testChatID)

	// Tighten to 1h, then reset to default via SetMaxAge(0).
	n.SetMaxAge(0)

	// Job posted 10h ago — fresh under default 48h.
	postedAt := time.Now().Add(-10 * time.Hour)
	n.NotifyNewJob(hunt.Job{Title: "fresh-under-default", URL: "https://jobs.io/3", Source: "greenhouse", PostedAt: &postedAt}, nil)
	drainDispatch()
	assert.Equal(t, int32(1), stub.callCount.Load(), "10h-old job sends after SetMaxAge(0) resets to 48h default")
}
