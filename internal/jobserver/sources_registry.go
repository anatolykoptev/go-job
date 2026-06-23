package jobserver

import (
	"errors"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/anatolykoptev/go_job/internal/engine/jobs/connectors"
)

// jobRegistry is the authoritative registry of all job search connectors.
// Initialized by initJobRegistry(), called from RegisterTools().
var jobRegistry *connectors.Registry //nolint:gochecknoglobals // package-level singleton, init-once

func initJobRegistry() {
	jobRegistry = connectors.BuildDefaultRegistry()

	// Wire ADR-J3 sentinel-error classifiers into engine.PlatformOutcome.
	// jobserver imports both engine and jobs so it is the natural wiring site;
	// engine cannot import jobs directly (cycle: jobs → engine).
	engine.RegisterPlatformOutcomeHooks(
		func(err error) bool { return errors.Is(err, jobs.ErrNoAPIKey) },
		func(err error) bool { return errors.Is(err, jobs.ErrParse) },
	)
}
