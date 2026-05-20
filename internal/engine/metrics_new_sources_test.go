package engine

import (
	"testing"
)

// TestNewSourceCountersRegistered verifies that the three new source counters
// are accessible via their Incr helpers and appear in the GetMetrics snapshot.
// Registry only includes written counters in Snapshot, so we call Incr first.
func TestNewSourceCountersRegistered(t *testing.T) {
	Init(Config{})
	IncrSherlockRequests()
	IncrCantinaRequests()
	IncrCode4renaRequests()

	snapshot := GetMetrics()
	for _, name := range []string{
		MetricSherlockRequests,
		MetricCantinaRequests,
		MetricCode4renaRequests,
	} {
		if _, ok := snapshot[name]; !ok {
			t.Errorf("metric %s not registered", name)
		}
	}
}

// TestIncrNewSourceCounters verifies that each Incr helper bumps its counter by exactly 1.
func TestIncrNewSourceCounters(t *testing.T) {
	Init(Config{})
	before := GetMetrics()
	IncrSherlockRequests()
	IncrCantinaRequests()
	IncrCode4renaRequests()
	after := GetMetrics()
	for _, name := range []string{MetricSherlockRequests, MetricCantinaRequests, MetricCode4renaRequests} {
		if after[name]-before[name] != 1 {
			t.Errorf("counter %s did not increment", name)
		}
	}
}
