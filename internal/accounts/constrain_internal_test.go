package accounts

import (
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecordConstrainRefusal_IncrementsCounterAndFiresHook(t *testing.T) {
	t.Cleanup(func() { SetConstrainRefusalHook(nil) })

	before := ConstrainRefusalsTotal()
	var hookHits atomic.Int64
	SetConstrainRefusalHook(func() { hookHits.Add(1) })

	recordConstrainRefusal()
	assert.Equal(t, before+1, ConstrainRefusalsTotal())
	assert.Equal(t, int64(1), hookHits.Load())

	SetConstrainRefusalHook(nil)
	recordConstrainRefusal()
	assert.Equal(t, before+2, ConstrainRefusalsTotal())
	assert.Equal(t, int64(1), hookHits.Load(), "clearing hook must stop callbacks while counter still increments")
}

func TestConstrain_DegradeCommentsAndHookWiring_SourceGate(t *testing.T) {
	constrainSrc, err := os.ReadFile("constrain.go")
	require.NoError(t, err)
	cs := string(constrainSrc)

	assert.NotContains(t, cs, "fails the\n//     boot")
	assert.NotContains(t, cs, "fails the\n// boot")
	assert.NotContains(t, cs, "fails the boot")
	assert.NotContains(t, cs, "refuses the boot")
	assert.GreaterOrEqual(t, strings.Count(cs, "recordConstrainRefusal()"), 3,
		"recordConstrainRefusal must be defined once and called in both ConstrainAccountColumns and EnsurePasswordRequired")

	mainSrc, err := os.ReadFile("../../main.go")
	require.NoError(t, err)
	ms := string(mainSrc)

	hookIdx := strings.Index(ms, "accounts.SetConstrainRefusalHook(engine.IncrAccountsConstrainRefusals)")
	bootIdx := strings.Index(ms, "bootstrapAccounts(")
	require.Positive(t, hookIdx, "initEngine must wire accounts.SetConstrainRefusalHook")
	require.Positive(t, bootIdx, "bootstrapAccounts call missing from main.go")
	assert.Less(t, hookIdx, bootIdx, "SetConstrainRefusalHook must be wired before bootstrapAccounts runs")
}
