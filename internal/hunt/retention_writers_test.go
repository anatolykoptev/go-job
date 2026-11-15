package hunt_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// TestRetentionWriters_AllTakeSharedLock derives the writer list from source
// instead of a hand-kept list: every non-test function under internal/ that
// inserts into hunt_ratings or hunt_retention_pins (directly or through a const
// holding the SQL) must call execRetentionShared or lockRetentionShared.
// Mutation: swap execRetentionShared for pool.Exec in any writer, or add a new
// unlocked INSERT, and this goes RED naming the function.
func TestRetentionWriters_AllTakeSharedLock(t *testing.T) {
	isWrite := func(s string) bool {
		return strings.Contains(s, "INSERT INTO hunt_ratings") || strings.Contains(s, "INSERT INTO hunt_retention_pins")
	}
	fset := token.NewFileSet()
	var files []*ast.File
	err := filepath.WalkDir("..", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		f, perr := parser.ParseFile(fset, p, nil, 0)
		files = append(files, f)
		return perr
	})
	if err != nil {
		t.Fatal(err)
	}
	writeConsts := map[string]bool{} // package-level consts whose literal is a write
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			if vs, ok := n.(*ast.ValueSpec); ok {
				for i, v := range vs.Values {
					if bl, ok := v.(*ast.BasicLit); ok && isWrite(bl.Value) && i < len(vs.Names) {
						writeConsts[vs.Names[i].Name] = true
					}
				}
			}
			return true
		})
	}
	writers := 0
	for _, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			writes, locked := false, false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.BasicLit:
					writes = writes || isWrite(x.Value)
				case *ast.Ident:
					writes = writes || writeConsts[x.Name]
					locked = locked || x.Name == "execRetentionShared" || x.Name == "lockRetentionShared"
				}
				return true
			})
			if writes {
				writers++
				if !locked {
					t.Errorf("%s: writes hunt_ratings/hunt_retention_pins without execRetentionShared/lockRetentionShared (see retentionLockKey)", fn.Name.Name)
				}
			}
		}
	}
	if writers < 7 { // Rate, RateExact, SetTriage, SetStage, ToggleShortlistStar, PinJob, BackfillArtifactMarkers
		t.Fatalf("scan found only %d writers: the oracle is blind", writers)
	}
}
