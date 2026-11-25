package vo

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strings"
	"testing"
)

// The files operation's two types say "this list came from the configuration"
// and "this path was matched against it" only as long as nothing in this
// package builds one any other way. Go cannot restrict a struct literal to one
// function, so this file reads the package's own source with go/ast and holds
// that property instead (ADR 0012).

// filesTypeConstructors pairs each guarded type with the one function allowed
// to fill in its fields.
var filesTypeConstructors = map[string]string{
	"FilesAllowList": "ParseFilesAllowList",
	"FilesPath":      "ParseFilesPath",
}

// filesTypeProducers is the complete list of exported functions and methods of
// this package whose results mention a guarded type. Adding one fails the test
// below until it is added here, in review.
var filesTypeProducers = []string{"ParseFilesAllowList", "ParseFilesPath"}

func TestFilesTypesAreBuiltOnlyByTheirConstructors(t *testing.T) {
	if v := filesConstructionViolations(nonTestFiles(t)); len(v) > 0 {
		t.Errorf("guarded files types are built outside their constructors:\n%s", strings.Join(v, "\n"))
	}
}

// TestFilesConstructionGuardCatchesViolations feeds the checker source that
// breaks each rule, so a checker that stopped looking would fail here rather
// than pass everything.
func TestFilesConstructionGuardCatchesViolations(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
	}{
		{"helper fills FilesPath", `package vo
func unchecked(raw string) FilesPath { return FilesPath{s: raw} }`},
		{"helper fills FilesAllowList positionally", `package vo
func listOf(xs []string) FilesAllowList { return FilesAllowList{xs} }`},
		{"pointer literal in a method", `package vo
func (l FilesAllowList) widen() *FilesAllowList { return &FilesAllowList{entries: nil} }`},
		{"exported producer outside the list", `package vo
func ParseFilesPathUnchecked(raw string) (FilesPath, error) { var p FilesPath; return p, nil }`},
		{"exported method producing a path", `package vo
func (l FilesAllowList) First() FilesPath { var p FilesPath; return p }`},
		{"conversion to FilesPath", `package vo
type raw struct{ s string }
func convert(r raw) FilesPath { return FilesPath(r) }`},
		{"pointer receiver on FilesPath", `package vo
func (p *FilesPath) reset(s string) { p.s = s }`},
		{"field written outside the constructor", `package vo
func mutate(p FilesPath) FilesPath { p.s = "/etc"; return p }`},
		{"literal in a package-level var", `package vo
var everything = FilesAllowList{entries: []string{"/"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := parser.ParseFile(token.NewFileSet(), "violation.go", tc.src, parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if v := filesConstructionViolations([]*ast.File{f}); len(v) == 0 {
				t.Errorf("checker accepted:\n%s", tc.src)
			}
		})
	}
}

// filesConstructionViolations applies four rules to the given files of this
// package:
//
//   - a composite literal of a guarded type with any element in it appears
//     only inside that type's constructor;
//   - no function converts a value to a guarded type;
//   - no method of a guarded type has a pointer receiver, and no assignment
//     writes a guarded type's fields outside its constructor;
//   - the exported functions and methods whose results mention a guarded type
//     are exactly filesTypeProducers.
func filesConstructionViolations(files []*ast.File) []string {
	var out []string
	inspect := func(scope string, n ast.Node) {
		ast.Inspect(n, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.CompositeLit:
				typ, ok := v.Type.(*ast.Ident)
				if !ok {
					return true
				}
				if ctor, guarded := filesTypeConstructors[typ.Name]; guarded && len(v.Elts) > 0 && scope != ctor {
					out = append(out, scope+": fills a "+typ.Name+" literal")
				}
			case *ast.CallExpr:
				if id, ok := v.Fun.(*ast.Ident); ok {
					if _, guarded := filesTypeConstructors[id.Name]; guarded {
						out = append(out, scope+": converts a value to "+id.Name)
					}
				}
			case *ast.AssignStmt:
				for _, lhs := range v.Lhs {
					sel, ok := lhs.(*ast.SelectorExpr)
					if !ok {
						continue
					}
					if sel.Sel.Name == "s" || sel.Sel.Name == "entries" || sel.Sel.Name == "n" {
						if !slices.Contains(filesTypeProducers, scope) {
							out = append(out, scope+": writes the field "+sel.Sel.Name)
						}
					}
				}
			}
			return true
		})
	}
	var producers []string
	for _, f := range files {
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				inspect(d.Name.Name, d)
				if d.Recv != nil {
					if star, ok := d.Recv.List[0].Type.(*ast.StarExpr); ok {
						if id, ok := star.X.(*ast.Ident); ok {
							if _, guarded := filesTypeConstructors[id.Name]; guarded {
								out = append(out, d.Name.Name+": has a pointer receiver on "+id.Name)
							}
						}
					}
				}
				if d.Name.IsExported() && resultsMentionFilesType(d) {
					producers = append(producers, d.Name.Name)
				}
			case *ast.GenDecl:
				inspect("package scope", d)
			}
		}
	}
	slices.Sort(producers)
	want := slices.Clone(filesTypeProducers)
	slices.Sort(want)
	if !slices.Equal(producers, want) {
		// Only the extra ones are a violation: a checker fed a single
		// synthetic file sees none of the real producers.
		for _, p := range producers {
			if !slices.Contains(want, p) {
				out = append(out, p+": exported, and its results mention a guarded files type")
			}
		}
	}
	return out
}

func resultsMentionFilesType(fn *ast.FuncDecl) bool {
	if fn.Type.Results == nil {
		return false
	}
	found := false
	for _, r := range fn.Type.Results.List {
		ast.Inspect(r.Type, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok {
				if _, guarded := filesTypeConstructors[id.Name]; guarded {
					found = true
				}
			}
			return !found
		})
	}
	return found
}

// TestFilesTypeProducersAreAllPresent is the other half of the list check: the
// real package declares every producer the list names, so the list cannot go
// stale with a name nothing declares.
func TestFilesTypeProducersAreAllPresent(t *testing.T) {
	var found []string
	for _, f := range nonTestFiles(t) {
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.IsExported() && resultsMentionFilesType(fn) {
				found = append(found, fn.Name.Name)
			}
		}
	}
	slices.Sort(found)
	want := slices.Clone(filesTypeProducers)
	slices.Sort(want)
	if !slices.Equal(found, want) {
		t.Errorf("exported producers of the files types are %q, want %q", found, want)
	}
}

// nonTestFiles parses this package's non-test sources.
func nonTestFiles(t *testing.T) []*ast.File {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		t.Fatal("no non-test sources found")
	}
	return files
}
