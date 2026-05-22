package oversize

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockStorer is a test double for the Storer interface.
type mockStorer struct {
	saved []Entry
	retID int64
	err   error
}

func (m *mockStorer) Save(_ context.Context, e Entry) (int64, error) {
	if m.err != nil {
		return 0, m.err
	}
	m.saved = append(m.saved, e)
	return m.retID, nil
}

func TestMaybeSpill_SmallPayload_Passthrough(t *testing.T) {
	t.Setenv("GO_JOB_OVERSIZE_THRESHOLD_BYTES", "1000")
	store := &mockStorer{retID: 42}
	ctx := context.Background()

	payload := map[string]string{"key": "value"}
	result, err := MaybeSpill(ctx, store, "test_tool", payload)

	require.NoError(t, err)
	// Small payload: returned as-is (original map, not envelope)
	got, ok := result.(map[string]string)
	require.True(t, ok, "expected original map type, got %T", result)
	assert.Equal(t, payload, got)
	// Store must NOT have been called
	assert.Len(t, store.saved, 0)
}

func TestMaybeSpill_LargePayload_Spills(t *testing.T) {
	// Set a very low threshold so our payload exceeds it
	t.Setenv("GO_JOB_OVERSIZE_THRESHOLD_BYTES", "10")
	store := &mockStorer{retID: 99}
	ctx := context.Background()

	payload := map[string]string{"key": strings.Repeat("x", 100)}
	result, err := MaybeSpill(ctx, store, "my_tool", payload)

	require.NoError(t, err)
	env, ok := result.(*Envelope)
	require.True(t, ok, "expected *Envelope, got %T", result)
	assert.Equal(t, int64(99), env.OversizeID)
	assert.Equal(t, "my_tool", env.ToolName)
	assert.Equal(t, "oversize_get", env.RetrieveVia)
	assert.NotEmpty(t, env.SHA256)
	assert.Greater(t, env.SizeBytes, 10)

	// Verify SHA256 matches actual marshalled payload
	raw, _ := json.Marshal(payload)
	sum := sha256.Sum256(raw)
	assert.Equal(t, hex.EncodeToString(sum[:]), env.SHA256)

	require.Len(t, store.saved, 1)
	assert.Equal(t, "my_tool", store.saved[0].ToolName)
	assert.Equal(t, env.SizeBytes, store.saved[0].SizeBytes)
}

func TestMaybeSpill_EnvOverride(t *testing.T) {
	// Lower threshold via env: 100 bytes
	t.Setenv("GO_JOB_OVERSIZE_THRESHOLD_BYTES", "100")
	store := &mockStorer{retID: 1}
	ctx := context.Background()

	// Build a payload that is > 100 bytes when marshalled
	payload := map[string]string{"data": strings.Repeat("y", 200)}
	result, err := MaybeSpill(ctx, store, "tool_x", payload)
	require.NoError(t, err)
	_, ok := result.(*Envelope)
	assert.True(t, ok, "payload > 100 bytes should spill; got %T", result)
	assert.Len(t, store.saved, 1)

	// Reset: payload that is < 100 bytes should pass through
	store2 := &mockStorer{retID: 2}
	small := map[string]string{"x": "y"}
	result2, err2 := MaybeSpill(ctx, store2, "tool_x", small)
	require.NoError(t, err2)
	_, ok2 := result2.(*Envelope)
	assert.False(t, ok2, "small payload should not spill")
	assert.Len(t, store2.saved, 0)
}

func TestMaybeSpill_ItemCount_SliceShape(t *testing.T) {
	t.Setenv("GO_JOB_OVERSIZE_THRESHOLD_BYTES", "10")
	items := make([]any, 7)
	for i := range items {
		items[i] = map[string]string{"id": "x"}
	}
	store := &mockStorer{retID: 5}
	result, err := MaybeSpill(context.Background(), store, "list_tool", items)
	require.NoError(t, err)
	env, ok := result.(*Envelope)
	require.True(t, ok)
	assert.Equal(t, 7, env.ItemCount)
}

func TestMaybeSpill_ItemCount_KnownMapKey(t *testing.T) {
	t.Setenv("GO_JOB_OVERSIZE_THRESHOLD_BYTES", "10")
	payload := map[string]any{
		"bounties": []any{"a", "b", "c", "d"},
	}
	store := &mockStorer{retID: 7}
	result, err := MaybeSpill(context.Background(), store, "bounty_tool", payload)
	require.NoError(t, err)
	env, ok := result.(*Envelope)
	require.True(t, ok)
	assert.Equal(t, 4, env.ItemCount)
}

func TestMaybeSpill_StoreError_Bubbled(t *testing.T) {
	t.Setenv("GO_JOB_OVERSIZE_THRESHOLD_BYTES", "10")
	storeErr := errors.New("pg: connection refused")
	store := &mockStorer{err: storeErr}

	payload := map[string]string{"data": strings.Repeat("z", 200)}
	_, err := MaybeSpill(context.Background(), store, "tool", payload)
	require.Error(t, err)
	assert.ErrorContains(t, err, "pg: connection refused")
}
