package engine

import (
	"testing"

	kitmetrics "github.com/anatolykoptev/go-kit/metrics"
)

// TestMetricHuntList_ConstDefined verifies the metric constant name.
func TestMetricHuntList_ConstDefined(t *testing.T) {
	if MetricHuntList != "hunt_list_total" {
		t.Errorf("MetricHuntList = %q, want %q", MetricHuntList, "hunt_list_total")
	}
}

// TestIncrHuntList_LabelBoundedAndLands swaps the package registry for a fresh
// in-memory one and asserts: (a) the four valid list kinds land under the
// labelled key, (b) an arbitrary kind is dropped (no cardinality leak). The
// list-tool kinds are PLURAL (jobs/bounties/...) — distinct from the singular
// ingest kinds — so this guards against the two allowlists drifting apart.
func TestIncrHuntList_LabelBoundedAndLands(t *testing.T) {
	orig := reg
	t.Cleanup(func() { reg = orig })
	reg = kitmetrics.NewRegistry()

	for _, k := range []string{"jobs", "bounties", "freelance", "security"} {
		IncrHuntList(k)
	}
	IncrHuntList("job")     // singular ingest kind — must NOT be accepted here
	IncrHuntList("garbage") // arbitrary — must be dropped

	snap := reg.Snapshot()
	for _, k := range []string{"jobs", "bounties", "freelance", "security"} {
		key := MetricHuntList + "{kind=" + k + "}"
		if snap[key] != 1 {
			t.Errorf("%s = %d, want 1", key, snap[key])
		}
	}
	if snap[MetricHuntList+"{kind=job}"] != 0 {
		t.Errorf("singular 'job' must not be a valid hunt_list kind (allowlist drift)")
	}
	if snap[MetricHuntList+"{kind=garbage}"] != 0 {
		t.Errorf("arbitrary kind 'garbage' leaked into metrics (cardinality risk)")
	}
}
