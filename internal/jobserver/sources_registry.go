package jobserver

import (
	"github.com/anatolykoptev/go_job/internal/engine/jobs/connectors"
)

// jobRegistry is the authoritative registry of all job search connectors.
// Initialized by initJobRegistry(), called from RegisterTools().
var jobRegistry *connectors.Registry //nolint:gochecknoglobals // package-level singleton, init-once

func initJobRegistry() {
	jobRegistry = connectors.BuildDefaultRegistry()
}
