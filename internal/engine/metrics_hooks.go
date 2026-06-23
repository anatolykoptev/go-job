package engine

// RegisterPlatformOutcomeHooks wires sentinel-error checkers from the jobs package
// into PlatformOutcome without creating an import cycle (engine → jobs would cycle
// since jobs → engine). Call once from jobserver init (after both packages are loaded).
//
// ADR-J3: the hooks replace the no-op defaults set in metrics.go so that
// errors.Is(err, jobs.ErrNoAPIKey) and errors.Is(err, jobs.ErrParse) produce
// outcome=no_key and outcome=parse_fail respectively.
func RegisterPlatformOutcomeHooks(noAPIKey func(error) bool, parseFail func(error) bool) {
	isNoAPIKeyErr = noAPIKey
	isParseErr = parseFail
}
