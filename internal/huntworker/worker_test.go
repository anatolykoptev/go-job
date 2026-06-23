package huntworker

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseQueries_Basic(t *testing.T) {
	got := parseQueries("golang developer, backend engineer, ")
	assert.Equal(t, []string{"golang developer", "backend engineer"}, got)
}

func TestParseQueries_Empty_UsesDefault(t *testing.T) {
	got := parseQueries("")
	assert.NotEmpty(t, got)
	// Default must not contain any ATS slugs (fitness function: no boards.greenhouse.io/X literals).
	for _, q := range got {
		assert.NotContains(t, q, "boards.greenhouse.io/")
		assert.NotContains(t, q, "jobs.lever.co/")
		assert.NotContains(t, q, "jobs.ashbyhq.com/")
	}
}

func TestHuntIngestEnabled_DefaultFalse(t *testing.T) {
	// HUNT_INGEST_ENABLED is not set in the test environment.
	t.Setenv("HUNT_INGEST_ENABLED", "")
	assert.False(t, huntIngestEnabled())
}

func TestHuntIngestEnabled_TrueWhenSet(t *testing.T) {
	t.Setenv("HUNT_INGEST_ENABLED", "true")
	assert.True(t, huntIngestEnabled())
}

func TestNewWorker_NilStore_ReturnsNil(t *testing.T) {
	w := NewWorker(nil)
	assert.Nil(t, w)
}

// TestNoPersonalSlugsInDefaults is the fitness function (ADR-002 / P1 design):
// the default HUNT_INGEST_QUERIES must contain only generic role strings,
// no ATS-slug-shaped literals like boards.greenhouse.io/<company>.
func TestNoPersonalSlugsInDefaults(t *testing.T) {
	queries := parseQueries(defaultIngestQueries)
	for _, q := range queries {
		assert.NotContains(t, q, "boards.greenhouse.io/",
			"default queries must not contain Greenhouse company slugs")
		assert.NotContains(t, q, "jobs.lever.co/",
			"default queries must not contain Lever company slugs")
		assert.NotContains(t, q, "jobs.ashbyhq.com/",
			"default queries must not contain Ashby company slugs")
		assert.NotContains(t, q, "seedOrgs",
			"default queries must not contain a seed org list")
	}
}
