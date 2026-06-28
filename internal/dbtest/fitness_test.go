package dbtest_test

// Anti-regrowth fitness gate: every *_test.go file in the module that reads
// DATABASE_URL and opens a DB connection must also reference RequireTestDB.
//
// Adding a new DB-opening test file without the guard will make this test RED.
// Falsification: remove the RequireTestDB call from any wired opener and this
// test fails, listing the offending path.

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// openerPatterns are the call patterns that indicate a DB connection is being
// opened in a test file. ConnectResumeDB is included because upwork_*_test.go
// files open the DB through that wrapper rather than pgxpool directly.
var openerPatterns = []string{
	"pgxpool.New(",
	"pgxpool.ParseConfig(",
	"pgx.Connect(",
	"ConnectResumeDB(",
}

func TestFitness_AllDBOpenersUseRequireTestDB(t *testing.T) {
	root, err := findModuleRoot()
	if err != nil {
		t.Fatalf("find module root: %v", err)
	}

	var offenders []string
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if d.Name() == "vendor" || d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		// Allowlist: the dbtest package itself contains RequireTestDB's own tests
		// and fitness gate — exclude it from self-checking.
		rel, _ := filepath.Rel(root, path)
		if strings.HasPrefix(rel, "internal/dbtest/") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		content := string(data)

		// Only check files that actually reference DATABASE_URL.
		if !strings.Contains(content, `"DATABASE_URL"`) {
			return nil
		}
		// And that open a DB connection.
		opensConn := false
		for _, p := range openerPatterns {
			if strings.Contains(content, p) {
				opensConn = true
				break
			}
		}
		if !opensConn {
			return nil
		}
		// Must import the dbtest package (import path is harder to land in a comment
		// by accident than just the function name "RequireTestDB").
		if !strings.Contains(content, `"github.com/anatolykoptev/go_job/internal/dbtest"`) {
			offenders = append(offenders, rel)
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk %s: %v", root, walkErr)
	}
	if len(offenders) > 0 {
		t.Errorf(
			"DB-opening test files missing RequireTestDB guard"+
				" (add dbtest.RequireTestDB(t, dsn) and import internal/dbtest):\n  %s",
			strings.Join(offenders, "\n  "),
		)
	}
}

// findModuleRoot walks upward from the test's working directory until it finds
// a directory containing go.mod. Tests in internal/dbtest/ run with cwd set
// to the package directory, so we need to ascend.
func findModuleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getwd: %w", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found starting from %s", dir)
		}
		dir = parent
	}
}
