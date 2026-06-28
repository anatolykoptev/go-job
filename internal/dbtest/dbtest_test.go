package dbtest

// Regression tests for RequireTestDB.
//
// These use a fakeTB (implements testingTB) so they run without any live
// database — the unit under test is the guard logic, not the DB connection.
//
// Falsification: if RequireTestDB is reverted to a bare `if dsn=="" { skip }`,
// TestRequireTestDB_ProdDSN_Fatals goes RED (Fatalf does not fire).

import (
	"fmt"
	"strings"
	"testing"
)

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
