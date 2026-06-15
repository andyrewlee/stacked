package e2e

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestEveryE2ETestIsParallel enforces that every top-level Test* in this package
// calls t.Parallel() as its first statement, so the subprocess-bound e2e suite
// keeps running concurrently as it grows. A test may opt out with a
// "// not-parallel: <reason>" doc comment. This is review-proof: a new serial
// test fails CI rather than silently slowing the suite.
func TestEveryE2ETestIsParallel(t *testing.T) {
	t.Parallel()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	entries, err := os.ReadDir(filepath.Dir(thisFile))
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(filepath.Dir(thisFile), e.Name())
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", e.Name(), err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !isTopLevelTestFunc(fn) {
				continue
			}
			if fn.Doc != nil && hasNotParallelDirective(fn.Doc.Text()) {
				continue
			}
			if !firstStmtCallsParallel(fn) {
				t.Errorf("%s: %s must call t.Parallel() as its first statement (or opt out with a // not-parallel: comment)", e.Name(), fn.Name.Name)
			}
		}
	}
}

// isTopLevelTestFunc reports whether fn is a Go test function (TestXxx(t *testing.T)),
// excluding TestMain.
func isTopLevelTestFunc(fn *ast.FuncDecl) bool {
	if !strings.HasPrefix(fn.Name.Name, "Test") || fn.Name.Name == "TestMain" {
		return false
	}
	if fn.Type.Params == nil || len(fn.Type.Params.List) != 1 {
		return false
	}
	star, ok := fn.Type.Params.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "T"
}

// hasNotParallelDirective reports whether a doc comment carries a structured
// "// not-parallel: <reason>" opt-out line (fn.Doc.Text() has already stripped
// the // and leading space). Matched as a line prefix, not any incidental
// mention of the word, so a comment like "not-parallel-safe legacy path" does
// not silently exempt the test.
func hasNotParallelDirective(doc string) bool {
	for _, line := range strings.Split(doc, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "not-parallel:") {
			return true
		}
	}
	return false
}

// firstStmtCallsParallel reports whether fn's body begins with a call to
// t.Parallel() on the test's own *testing.T parameter (so x.Parallel() or a
// shadowed receiver does not satisfy the guard).
func firstStmtCallsParallel(fn *ast.FuncDecl) bool {
	if fn.Body == nil || len(fn.Body.List) == 0 {
		return false
	}
	expr, ok := fn.Body.List[0].(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := expr.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Parallel" {
		return false
	}
	recv, ok := sel.X.(*ast.Ident)
	return ok && recv.Name != "" && recv.Name == testParamName(fn)
}

// testParamName returns the name of a test function's sole *testing.T parameter,
// or "" if it has none or it is the blank identifier.
func testParamName(fn *ast.FuncDecl) string {
	if fn.Type.Params == nil || len(fn.Type.Params.List) != 1 {
		return ""
	}
	names := fn.Type.Params.List[0].Names
	if len(names) != 1 || names[0].Name == "_" {
		return ""
	}
	return names[0].Name
}
