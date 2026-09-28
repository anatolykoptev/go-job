package main

// Fitness gates for the gojob-admin CLI — assertions about the SOURCE, not
// runtime behaviour, catching the failure class that would be silent in
// production: a code path that manages to write role='owner'.

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFitness_NoOwnerRoleLiteral: no non-test source file in cmd/gojob-admin
// may contain the string literal "owner" — so no code path can pass it as a
// role value. Help text mentioning the forbidden role uses 'owner' quoting,
// which this gate deliberately allows.
//
// Falsification: insert `"owner"` as a role value in any command file and
// this test goes RED, naming the file.
func TestFitness_NoOwnerRoleLiteral(t *testing.T) {
	var offenders []string
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || strings.HasSuffix(path, "_test.go") || !strings.HasSuffix(path, ".go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), `"owner"`) {
			offenders = append(offenders, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk cmd/gojob-admin: %v", err)
	}
	if len(offenders) > 0 {
		t.Errorf("files containing the role literal \"owner\" (must be impossible to write):\n  %s",
			strings.Join(offenders, "\n  "))
	}
}

// TestFitness_RoleAllowlist: the single validation point for --role accepts
// exactly {user, admin} — every other string, including the DB-CHECK-banned
// superuser role, is rejected before any SQL runs.
func TestFitness_RoleAllowlist(t *testing.T) {
	for _, ok := range []string{"user", "admin"} {
		if err := validateRole(ok); err != nil {
			t.Errorf("role %q must be accepted, got %v", ok, err)
		}
	}
	for _, bad := range []string{"owner", "superadmin", "User", "", "admin ", "root"} {
		if err := validateRole(bad); err == nil {
			t.Errorf("role %q must be rejected", bad)
		}
	}
}
