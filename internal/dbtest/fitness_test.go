package dbtest_test

// Anti-regrowth fitness gate (AST based, so comments, aliases, LookupEnv and
// const-named env vars cannot dodge it).
//
// Rules, per function declaration in every *_test.go of the module:
//  1. A function that reads a database DSN environment variable (os.Getenv or
//     os.LookupEnv, literal name or a const resolved in the same package) must
//     call the scratch guard (RequireTestDB).
//  2. A function that opens a database (pgxpool.New, pgxpool.NewWithConfig,
//     pgx.Connect, pgx.ConnectConfig, sql.Open) must call the guard, or take
//     the DSN as a string parameter so its caller (rule 1) owns the guard.
//     Opening with a hard-coded postgres:// literal is never allowed.
//
// Falsification: delete the guard call from any helper that reads a DSN
// variable, or add a function that opens a database from nowhere, and this
// test fails naming file and function.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	guardName = "RequireTestDB"
	modRoot   = "../.."
	selfFile  = "fitness_test.go"
)

var dsnEnvName = regexp.MustCompile(`(?i)(DATABASE_URL|_DSN$)`)

// pureReaders are helpers that only read the variable and hand the DSN to a
// caller that owns the guard. Each entry must exist (stale entries fail).
var pureReaders = map[string]string{}

// neverConnects are helpers that open a pool on a hard-coded unreachable
// address purely to satisfy a constructor; they cannot reach any server.
// Each entry must exist (stale entries fail).
var neverConnects = map[string]string{}

var openers = map[string]bool{
	"pgxpool.New": true, "pgxpool.NewWithConfig": true,
	"pgx.Connect": true, "pgx.ConnectConfig": true, "sql.Open": true,
}

type fnInfo struct {
	file, name        string
	readsEnv, opens   bool
	guards, hasString bool
	literalDSN        bool
	calls             map[string]bool
}

func TestFitness_DBOpenersUseScratchGuard(t *testing.T) {
	dirs := map[string][]string{}
	err := filepath.WalkDir(modRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); n == "vendor" || n == ".git" || n == "dbtest" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, "_test.go") && d.Name() != selfFile {
			dirs[filepath.Dir(path)] = append(dirs[filepath.Dir(path)], path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	var offenders []string
	seenPure := map[string]bool{}
	seenNever := map[string]bool{}
	for _, files := range dirs {
		fset := token.NewFileSet()
		var parsed []*ast.File
		consts := map[string]string{}
		for _, f := range files {
			af, err := parser.ParseFile(fset, f, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", f, err)
			}
			parsed = append(parsed, af)
			collectConsts(af, consts)
		}
		var infos []fnInfo
		for _, af := range parsed {
			for _, decl := range af.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				fi := inspect(fd, consts)
				fi.file = fset.Position(fd.Pos()).Filename
				infos = append(infos, fi)
			}
		}
		// A function is guarded when it calls the guard directly or calls a
		// guarded function of the same package (transitively).
		guarded := map[string]bool{}
		for changed := true; changed; {
			changed = false
			for _, fi := range infos {
				if guarded[fi.name] {
					continue
				}
				ok := fi.guards
				for c := range fi.calls {
					ok = ok || guarded[c]
				}
				if ok {
					guarded[fi.name] = true
					changed = true
				}
			}
		}
		for _, fi := range infos {
			if _, ok := pureReaders[fi.name]; ok && fi.readsEnv && !fi.opens {
				seenPure[fi.name] = true
				continue
			}
			if _, ok := neverConnects[fi.name]; ok && fi.opens {
				seenNever[fi.name] = true
				continue
			}
			switch {
			case fi.literalDSN && fi.opens:
				offenders = append(offenders, fi.file+": "+fi.name+" opens a database from a hard-coded postgres:// literal")
			case fi.readsEnv && !guarded[fi.name]:
				offenders = append(offenders, fi.file+": "+fi.name+" reads a DSN variable without "+guardName+" (directly or via a guarded helper)")
			case fi.opens && !guarded[fi.name] && !fi.hasString:
				offenders = append(offenders, fi.file+": "+fi.name+" opens a database without "+guardName+" or a DSN parameter")
			}
		}
	}
	for name := range pureReaders {
		if !seenPure[name] {
			offenders = append(offenders, "stale pureReaders entry (no such DSN-reading helper): "+name)
		}
	}
	for name := range neverConnects {
		if !seenNever[name] {
			offenders = append(offenders, "stale neverConnects entry (no such opener): "+name)
		}
	}
	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Errorf("DB test helpers bypass the scratch guard:\n  %s", strings.Join(offenders, "\n  "))
	}
}

func collectConsts(f *ast.File, out map[string]string) {
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || (gd.Tok != token.CONST && gd.Tok != token.VAR) {
			continue
		}
		for _, sp := range gd.Specs {
			vs := sp.(*ast.ValueSpec)
			for i, n := range vs.Names {
				if i < len(vs.Values) {
					if bl, ok := vs.Values[i].(*ast.BasicLit); ok && bl.Kind == token.STRING {
						if s, err := strconv.Unquote(bl.Value); err == nil {
							out[n.Name] = s
						}
					}
				}
			}
		}
	}
}

func inspect(fd *ast.FuncDecl, consts map[string]string) fnInfo {
	fi := fnInfo{name: fd.Name.Name, calls: map[string]bool{}}
	if fd.Type.Params != nil {
		for _, p := range fd.Type.Params.List {
			if id, ok := p.Type.(*ast.Ident); ok && id.Name == "string" {
				fi.hasString = true
			}
		}
	}
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			name := callName(x.Fun)
			if id, ok := x.Fun.(*ast.Ident); ok {
				fi.calls[id.Name] = true
			}
			switch {
			case name == "os.Getenv" || name == "os.LookupEnv":
				if len(x.Args) == 1 && dsnEnvName.MatchString(resolve(x.Args[0], consts)) {
					fi.readsEnv = true
				}
			case openers[name]:
				fi.opens = true
			case name == guardName || strings.HasSuffix(name, "."+guardName):
				fi.guards = true
			}
		case *ast.BasicLit:
			if x.Kind == token.STRING {
				if s, err := strconv.Unquote(x.Value); err == nil && (strings.HasPrefix(s, "postgres://") || strings.HasPrefix(s, "postgresql://")) {
					fi.literalDSN = true
				}
			}
		}
		return true
	})
	return fi
}

func callName(e ast.Expr) string {
	switch f := e.(type) {
	case *ast.SelectorExpr:
		if id, ok := f.X.(*ast.Ident); ok {
			return id.Name + "." + f.Sel.Name
		}
	case *ast.Ident:
		return f.Name
	}
	return ""
}

func resolve(e ast.Expr, consts map[string]string) string {
	switch x := e.(type) {
	case *ast.BasicLit:
		s, _ := strconv.Unquote(x.Value)
		return s
	case *ast.Ident:
		return consts[x.Name]
	}
	return ""
}
