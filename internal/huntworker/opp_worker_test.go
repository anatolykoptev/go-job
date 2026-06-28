package huntworker

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHuntOppIngestEnabled_Default(t *testing.T) {
	// Not set → defaults to true (operator wants it on).
	t.Setenv("HUNT_OPP_INGEST_ENABLED", "")
	assert.True(t, huntOppIngestEnabled(), "unset HUNT_OPP_INGEST_ENABLED must default to true")
}

func TestHuntOppIngestEnabled_ExplicitTrue(t *testing.T) {
	t.Setenv("HUNT_OPP_INGEST_ENABLED", "true")
	assert.True(t, huntOppIngestEnabled())
}

func TestHuntOppIngestEnabled_ExplicitFalse(t *testing.T) {
	t.Setenv("HUNT_OPP_INGEST_ENABLED", "false")
	assert.False(t, huntOppIngestEnabled())
}

func TestStartOpportunityWorker_NilStore_Noop(t *testing.T) {
	// Must not panic or start a goroutine when store is nil.
	t.Setenv("HUNT_OPP_INGEST_ENABLED", "true")
	// context.Background() intentionally not cancelled — noop returns immediately.
	StartOpportunityWorker(t.Context(), nil)
}

func TestStartOpportunityWorker_Disabled_Noop(t *testing.T) {
	t.Setenv("HUNT_OPP_INGEST_ENABLED", "false")
	// Would panic or block if it tried to start; it must noop.
	StartOpportunityWorker(t.Context(), nil)
}
