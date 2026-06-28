// Package dbtest provides test helpers for database integration tests.
// It is intended to be imported only from *_test.go files; it never links
// into the production binary because Go excludes test-only imports from
// non-test builds.
package dbtest

import (
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// testingTB is the minimal surface RequireTestDB needs. It is satisfied by
// *testing.T and *testing.B in real tests, and by the fake in dbtest_test.go
// for unit tests of this package.
//
// We define our own minimal interface rather than using testing.TB because
// testing.TB has an unexported method (private()), making it impossible to
// implement with a fake struct outside the testing package. Using our own
// interface also avoids importing "testing" here, which would be a no-op for
// test files but is cleaner.
type testingTB interface {
	Helper()
	Skipf(format string, args ...any)
	Fatalf(format string, args ...any)
}

// RequireTestDB validates that dsn points at a *_test database and returns the
// database name. Call at the top of every DB-integration test opener in place
// of a bare `if dsn == "" { t.Skip(...) }`.
//
// Guard semantics — by design:
//
//   - empty dsn → Skipf: DATABASE_URL is unset, which is the normal state in
//     CI without an integration Postgres. A skip is correct: the test is
//     legitimately not runnable, not misbehaving.
//
//   - dsn set, name NOT ending "_test" → Fatalf (LOUD refusal, not Skipf):
//     A silent skip here would let misconfigured CI believe the test ran and
//     passed while pointed at prod. Fatalf makes the misconfiguration
//     immediately visible and blocks the run.
//     Background: this guard exists to prevent the TEST_DATABASE_URL→prod
//     isolation failure class. In the triggering incident, test setup helpers
//     (TRUNCATE hunt_* CASCADE) destroyed ~500 real rows in the production
//     `gojob` database because DATABASE_URL was set to the prod DSN.
//
//   - dsn set, name ends "_test" → returns the database name; caller proceeds.
func RequireTestDB(tb testingTB, dsn string) string {
	tb.Helper()
	if dsn == "" {
		tb.Skipf("DATABASE_URL not set — skipping DB integration test")
		return ""
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		tb.Fatalf("dbtest.RequireTestDB: parse DSN: %v", err)
		return ""
	}
	name := cfg.ConnConfig.Database
	if !strings.HasSuffix(name, "_test") {
		tb.Fatalf(
			"dbtest.RequireTestDB: database %q does not end in \"_test\" — "+
				"refusing to run destructive tests against a non-test database. "+
				"Set DATABASE_URL to a *_test database (e.g. gojob_test). "+
				"Guard class: TEST_DATABASE_URL→prod isolation "+
				"(incident: TRUNCATE hunt_* CASCADE destroyed ~500 prod rows).",
			name,
		)
		return ""
	}
	return name
}
