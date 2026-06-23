package connectors_test

import (
	"strings"
	"testing"

	"github.com/anatolykoptev/go_job/internal/engine/jobs/connectors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// queryVariantsSource is the optional interface that ATS sources expose.
type queryVariantsSource interface {
	connectors.Source
	QueryVariants(base, loc string) []string
}

func TestATSSource_QueryVariantsDistinct(t *testing.T) {
	for _, provider := range []string{"lever", "greenhouse", "ashby"} {
		src := connectors.ATSSource(provider)
		vs, ok := src.(queryVariantsSource)
		require.True(t, ok, "ATSSource(%q) must implement QueryVariants", provider)

		variants := vs.QueryVariants("golang", "")
		require.GreaterOrEqual(t, len(variants), 2,
			"provider %s must return >=2 variants", provider)

		seen := make(map[string]bool, len(variants))
		for _, v := range variants {
			assert.False(t, seen[v],
				"provider %s has duplicate variant %q", provider, v)
			seen[v] = true
		}

		scope := src.SiteScope()
		host := strings.TrimPrefix(scope, "site:")
		for _, v := range variants {
			assert.Contains(t, v, host,
				"provider %s variant %q must contain board host %q",
				provider, v, host)
		}
	}
}

func TestATSSource_QueryVariantsWithLocation(t *testing.T) {
	src := connectors.ATSSource("lever")
	vs := src.(queryVariantsSource)

	noLoc := vs.QueryVariants("golang", "")
	withLoc := vs.QueryVariants("golang", "remote")

	require.Equal(t, len(noLoc), len(withLoc), "variant count must not change with location")

	for i, v := range withLoc {
		assert.Contains(t, v, "remote",
			"variant %d must contain location 'remote'; got %q", i, v)
		assert.NotEqual(t, noLoc[i], v,
			"variant %d must differ when location is provided", i)
	}
}
