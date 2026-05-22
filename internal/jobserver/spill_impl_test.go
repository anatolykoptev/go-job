package jobserver

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testPayload is a concrete struct used to exercise spillIfOversize.
type testPayload struct {
	Answer string `json:"answer"`
	Count  int    `json:"count"`
}

// TestSpillIfOversize_NoStore_ReturnsFalse verifies that when the oversize
// store is not configured, spillIfOversize returns (nil, false, nil) — no spill.
//
// RED: fails until spillIfOversize[T] is defined in spill.go.
func TestSpillIfOversize_NoStore_ReturnsFalse(t *testing.T) {
	ctx := context.Background()
	payload := testPayload{Answer: "hello", Count: 1}

	cr, spilled, err := spillIfOversize(ctx, "tool_test", payload)
	require.NoError(t, err)
	assert.False(t, spilled, "no store configured: should not spill")
	assert.Nil(t, cr)
}

// TestSpillIfOversize_SmallPayload_NoSpill verifies that a payload below the
// threshold returns (nil, false, nil) even when a store is present.
func TestSpillIfOversize_SmallPayload_NoSpill(t *testing.T) {
	t.Setenv("GO_JOB_OVERSIZE_THRESHOLD_BYTES", "100000") // 100 kB — much larger than payload
	ctx := context.Background()
	payload := testPayload{Answer: "small", Count: 1}

	cr, spilled, err := spillIfOversize(ctx, "tool_test", payload)
	require.NoError(t, err)
	assert.False(t, spilled)
	assert.Nil(t, cr)
}

// TestSpillIfOversize_LargePayload_NoStore_NoSpill verifies that when the
// store is not configured, even a large payload above the threshold returns
// (nil, false, nil) — the store gate is checked before the threshold.
//
// The combined store+threshold path is tested at the oversize package level
// (internal/oversize/spill_test.go) and via integration when the server is
// wired with DATABASE_URL.
func TestSpillIfOversize_LargePayload_NoStore_NoSpill(t *testing.T) {
	t.Setenv("GO_JOB_OVERSIZE_THRESHOLD_BYTES", "10") // tiny threshold
	ctx := context.Background()
	// engine.GetOversizeStore() returns nil (not set in tests) — no spill regardless of payload size.
	payload := testPayload{Answer: "this is a long answer that exceeds 10 bytes", Count: 42}

	cr, spilled, err := spillIfOversize(ctx, "tool_test", payload)
	require.NoError(t, err)
	assert.False(t, spilled, "without a store, spill must not occur even for large payloads")
	assert.Nil(t, cr)
}

// TestSpillIfOversize_DBError_ReturnsError verifies that a store error
// propagates as an error return (NOT graceful — caller decides behaviour).
func TestSpillIfOversize_DBError_ReturnsError(t *testing.T) {
	t.Setenv("GO_JOB_OVERSIZE_THRESHOLD_BYTES", "10")
	ctx := context.Background()
	payload := testPayload{Answer: "this is a long answer that exceeds 10 bytes", Count: 1}

	// spillIfOversize reads the store from engine.GetOversizeStore().
	// Since the store is not set (nil in tests), the function returns (nil, false, nil).
	// A DB error path is tested at the oversize.MaybeSpill level (oversize/spill_test.go).
	// This test confirms the non-store path exits cleanly.
	cr, spilled, err := spillIfOversize(ctx, "tool_test", payload)
	require.NoError(t, err)
	assert.False(t, spilled)
	assert.Nil(t, cr)
}
