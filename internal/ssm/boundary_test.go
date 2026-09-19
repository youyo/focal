package ssm

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// This file is internal/ssm's own version of internal/ssh's boundary_test.go:
// it reads this package's own source with go/ast and asserts, mechanically,
// the two properties command.go's and executor.go's doc comments claim. A
// change that widened either property fails here rather than only in review.

// nonTestGoFiles lists this package's own source files, excluding tests: the
// properties below are about the package's production code, not about the
// fixtures a test uses to exercise it.
func nonTestGoFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read internal/ssm: %v", err)
	}
	var files []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		files = append(files, name)
	}
	if len(files) == 0 {
		t.Fatal("found no non-test .go file in internal/ssm; the walk is broken")
	}
	return files
}

// TestCommandLineAndQuoteTokenAreUnexported pins that the two functions which
// turn an ssh.Command into text stay unexported: newCommand and buildArgv are
// internal/ssh's equivalent, and boundary_test.go there enforces the same
// thing by construction (an unexported Command field). Here it has to be
// asserted directly, because Go's own visibility rule is the only thing that
// would otherwise say so.
func TestCommandLineAndQuoteTokenAreUnexported(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "command.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse command.go: %v", err)
	}
	want := map[string]bool{"commandLine": false, "quoteToken": false}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil {
			continue
		}
		if _, tracked := want[fn.Name.Name]; tracked {
			want[fn.Name.Name] = true
			if fn.Name.IsExported() {
				t.Errorf("%s is exported; the shell line this package sends must be assembled by exactly one unexported function", fn.Name.Name)
			}
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("found no function named %s in command.go; the walk is broken", name)
		}
	}
}

// TestNoExportedFunctionTakesARawString is this package's own copy of the
// property internal/security_test.go checks from outside: an exported
// function that accepted a bare string could let a caller build the shell
// line commandLine assembles from something other than an already-validated
// ssh.Command. Keeping both copies is deliberate, the same reasoning
// commands.go gives for internal/ssh's boundary_test.go and security_test.go
// both existing: a change made only inside this package should not be able to
// silence the outside check by construction.
func TestNoExportedFunctionTakesARawString(t *testing.T) {
	fset := token.NewFileSet()
	checked := 0
	for _, name := range nonTestGoFiles(t) {
		file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !fn.Name.IsExported() || fn.Type.Params == nil {
				continue
			}
			checked++
			for _, param := range fn.Type.Params.List {
				if ident, ok := param.Type.(*ast.Ident); ok && ident.Name == "string" {
					t.Errorf("%s.%s takes a bare string parameter; every command this package runs must arrive as an ssh.Command",
						name, fn.Name.Name)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("found no exported function to check; the walk is broken")
	}
}
