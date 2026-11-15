package dbtest

// Regression tests for RequireTestDB.
//
// These use a fakeTB (implements testingTB) so they run without any live
// database — the unit under test is the guard logic, not the DB connection.
//
// Falsification: if RequireTestDB is reverted to a bare `if dsn=="" { skip }`,
// TestRequireTestDB_ProdDSN_Fatals goes RED (Fatalf does not fire).

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// stubScratch replaces the server-level check so the name-only unit tests below
// need no database. TestRequireTestDB_ServerCheck_* cover the wiring itself.
func stubScratch(t *testing.T, err error) {
	t.Helper()
	old := scratchCheck
	scratchCheck = func(context.Context, string) error { return err }
	t.Cleanup(func() { scratchCheck = old })
}

// fakeTB records whether Skipf / Fatalf fired and the last message.
type fakeTB struct {
	skipfFired  bool
	fatalfFired bool
	lastMsg     string
}

func (f *fakeTB) Helper() {}

func (f *fakeTB) Skipf(format string, args ...any) {
	f.skipfFired = true
	f.lastMsg = fmt.Sprintf(format, args...)
}

func (f *fakeTB) Fatalf(format string, args ...any) {
	f.fatalfFired = true
	f.lastMsg = fmt.Sprintf(format, args...)
}

// TestRequireTestDB_EmptyDSN_Skips: unset DATABASE_URL → Skipf, not Fatalf.
// This is the normal CI state when no integration Postgres is available.
func TestRequireTestDB_EmptyDSN_Skips(t *testing.T) {
	f := &fakeTB{}
	RequireTestDB(f, "")
	if !f.skipfFired {
		t.Error("expected Skipf to fire for empty DSN")
	}
	if f.fatalfFired {
		t.Error("Fatalf must NOT fire for empty DSN")
	}
}

// TestRequireTestDB_ProdDSN_Fatals: the exact incident DSN shape (db = "gojob",
// no _test suffix) must Fatalf — not Skipf — so the misconfiguration is visible.
func TestRequireTestDB_ProdDSN_Fatals(t *testing.T) {
	f := &fakeTB{}
	RequireTestDB(f, "postgres://u:p@h:5432/gojob")
	if !f.fatalfFired {
		t.Error("expected Fatalf for prod DSN (the exact incident DSN shape)")
	}
	if f.skipfFired {
		t.Error("Skipf must NOT fire for prod DSN: a skip silently passes CI while pointed at prod")
	}
	if !strings.Contains(f.lastMsg, "gojob") {
		t.Errorf("Fatalf message must name the database, got: %q", f.lastMsg)
	}
	if !strings.Contains(f.lastMsg, "_test") {
		t.Errorf("Fatalf message must mention _test requirement, got: %q", f.lastMsg)
	}
}

// TestRequireTestDB_TestDSN_Passes: a *_test DB name must pass and return the name.
func TestRequireTestDB_TestDSN_Passes(t *testing.T) {
	stubScratch(t, nil)
	f := &fakeTB{}
	name := RequireTestDB(f, "postgres://u:p@h:5432/gojob_test")
	if f.skipfFired {
		t.Error("Skipf must NOT fire for a _test DSN")
	}
	if f.fatalfFired {
		t.Errorf("Fatalf must NOT fire for a _test DSN; got msg: %q", f.lastMsg)
	}
	if name != "gojob_test" {
		t.Errorf("returned name = %q, want %q", name, "gojob_test")
	}
}

// TestRequireTestDB_MalformedDSN_Fatals: an unparseable DSN must Fatalf (parse path).
func TestRequireTestDB_MalformedDSN_Fatals(t *testing.T) {
	// A postgres:// URI with an unclosed bracket host triggers a URL parse error
	// in pgconn.ParseConfig, exercising the parse-failure Fatalf branch.
	f := &fakeTB{}
	RequireTestDB(f, "postgres://u@[invalid-bracket/db")
	if !f.fatalfFired {
		t.Error("expected Fatalf for malformed DSN")
	}
	if f.skipfFired {
		t.Error("Skipf must NOT fire for malformed DSN")
	}
}

// TestRequireTestDB_ServerCheck_RefusesSharedServer: a *_test name on a server
// that holds other databases must Fatalf, never pass and never skip.
func TestRequireTestDB_ServerCheck_RefusesSharedServer(t *testing.T) {
	stubScratch(t, errors.New("server h:5432 is not a scratch server: it holds non-test database(s) gojob"))
	f := &fakeTB{}
	name := RequireTestDB(f, "postgres://u:p@h:5432/gojob_test")
	if !f.fatalfFired {
		t.Fatal("expected Fatalf: *_test name on a server holding non-test databases")
	}
	if f.skipfFired {
		t.Error("Skipf must NOT fire: a skip would hide the refusal")
	}
	if name != "" {
		t.Errorf("must not return a name on refusal, got %q", name)
	}
	if !strings.Contains(f.lastMsg, "make test-db") {
		t.Errorf("message must point at the safe recipe, got %q", f.lastMsg)
	}
}

// TestRequireTestDB_ServerCheck_WiredToKit runs WITHOUT the stub: a *_test DSN
// whose server cannot be inspected (nothing listens on port 1) must Fatalf.
// It goes RED if the scratchCheck call is removed from RequireTestDB, because
// the name-only path would then accept the DSN.
func TestRequireTestDB_ServerCheck_WiredToKit(t *testing.T) {
	f := &fakeTB{}
	RequireTestDB(f, "postgres://u:p@127.0.0.1:1/gojob_test?sslmode=disable&connect_timeout=2")
	if !f.fatalfFired {
		t.Fatal("expected Fatalf: server behind the DSN could not be proven to be a scratch server")
	}
	if f.skipfFired {
		t.Error("Skipf must NOT fire")
	}
}
