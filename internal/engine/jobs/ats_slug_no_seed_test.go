package jobs

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var boardSlugLiteralRe = regexp.MustCompile(
	`(?:boards|job-boards)\.greenhouse\.io/[a-z0-9]{2,}` +
		`|jobs\.lever\.co/[a-z0-9]{2,}` +
		`|jobs\.ashbyhq\.com/[a-z0-9]{2,}`,
)

func TestNoHardcodedATSSlugs(t *testing.T) {
	root := findInternalRoot(t)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || filepath.Ext(path) != ".go" {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if loc := boardSlugLiteralRe.FindIndex(data); loc != nil {
			line := 1 + strings.Count(string(data[:loc[0]]), "\n")
			t.Errorf("hardcoded ATS slug at %s:%d — slug cache must be runtime-populated only (PUBLIC repo constraint)",
				path, line)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk internal/: %v", err)
	}
}

func findInternalRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "internal")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root (go.mod)")
		}
		dir = parent
	}
}
