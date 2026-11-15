// Package dbtest guards Postgres-backed tests against running on a server that
// holds real data.
//
// A database name ending in "_test" proves nothing: a "_test" database can be
// created on a production cluster, and the test suites that DROP and recreate
// tables or scratch databases will happily run there. MustBeScratch checks the
// property that actually distinguishes an ephemeral test server from a shared
// one: the server holds no database other than "postgres", the templates and
// databases whose name ends in "_test".
//
// There is deliberately no "the role is not a superuser" escape hatch. A
// restricted application role passes any role-based check and still sees the
// same pg_database list, so the catalog check is the only one that holds
// regardless of host, port, role or environment variable.
//
// A test server therefore must be provisioned for the test: a throwaway
// container, or a CI service container, whose only databases are "postgres" and
// "*_test" ones.
package dbtest

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	testSuffix    = "_test"
	maintenanceDB = "postgres"
	checkTimeout  = 15 * time.Second
)

// tb is the subset of testing.TB the guard uses; it lets the guard's own
// behaviour (skip vs fatal) be tested without a real *testing.T.
type tb interface {
	Helper()
	Skipf(format string, args ...any)
	Fatalf(format string, args ...any)
}

// lister returns the names of all non-template databases on the server cfg
// points at. It is a seam so the policy can be tested against a fake catalog.
type lister func(ctx context.Context, cfg *pgx.ConnConfig) ([]string, error)

// MustBeScratch skips t when dsn is empty and fails it (never skips) unless dsn
// points at an ephemeral test server:
//
//   - the target database name ends in "_test", and
//   - the server holds no database other than "postgres", the templates and
//     names ending in "_test".
//
// A server that cannot be inspected is refused, so the guard fails closed.
// Call it before the first statement a test sends, including CREATE DATABASE
// through an admin DSN.
func MustBeScratch(t testing.TB, dsn string) {
	t.Helper()
	mustBeScratch(t, dsn, listDatabases)
}

func mustBeScratch(t tb, dsn string, list lister) {
	t.Helper()
	if dsn == "" {
		t.Skipf("no test database DSN set; skipping Postgres-backed test")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
	defer cancel()
	if err := check(ctx, dsn, list); err != nil {
		t.Fatalf("REFUSING to run: %v", err)
	}
}

// Check is the error-returning form of MustBeScratch, for TestMain and helpers
// that have no testing.TB. An empty dsn is an error here.
func Check(ctx context.Context, dsn string) error {
	return check(ctx, dsn, listDatabases)
}

func check(ctx context.Context, dsn string, list lister) error {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("cannot parse DSN: %w", err)
	}
	if !isTestName(cfg.Database) {
		return fmt.Errorf("database %q does not end in %q", cfg.Database, testSuffix)
	}
	dbs, err := list(ctx, cfg)
	if err != nil {
		return fmt.Errorf("cannot inspect server %s:%d to prove it is a scratch server: %w", cfg.Host, cfg.Port, err)
	}
	if err := checkCatalog(dbs); err != nil {
		return fmt.Errorf("server %s:%d is not a scratch server: %w", cfg.Host, cfg.Port, err)
	}
	return nil
}

// checkCatalog is the server-level policy: every non-template database must be
// the maintenance database or end in "_test".
func checkCatalog(dbs []string) error {
	var foreign []string
	for _, name := range dbs {
		if name != maintenanceDB && !isTestName(name) {
			foreign = append(foreign, name)
		}
	}
	if len(foreign) > 0 {
		return fmt.Errorf("it holds non-test database(s) %s; tests need an ephemeral server whose only databases are %q and \"*%s\" (start a throwaway container, see the repo's test-db target)",
			strings.Join(foreign, ", "), maintenanceDB, testSuffix)
	}
	return nil
}

func isTestName(name string) bool {
	return len(name) > len(testSuffix) && strings.HasSuffix(name, testSuffix)
}

// listDatabases connects to the maintenance database (the target may not exist
// yet) and reads the cluster-wide catalog.
func listDatabases(ctx context.Context, cfg *pgx.ConnConfig) ([]string, error) {
	c := cfg.Copy()
	c.Database = maintenanceDB
	conn, err := pgx.ConnectConfig(ctx, c)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close(ctx) }()
	rows, err := conn.Query(ctx, `SELECT datname FROM pg_database WHERE NOT datistemplate`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
