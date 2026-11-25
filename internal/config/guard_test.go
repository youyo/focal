package config

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The files allow list is trusted because it comes from a configuration file
// whose ownership and mode Load checked. Nothing in vo.FilesAllowList's type
// says so; the two tests below hold it in the source instead (ADR 0012):
//
//   - the exported functions that can produce a Config — and so an Operation
//     and a list — are exactly Load and LoadFrom, and both read through
//     readConfigFile, the function that checks the file's permissions;
//   - an exported function or method that returns a vo.FilesAllowList takes
//     no argument, so no wrapper here can turn a caller's input into a list.

// configProducers is the complete list of exported, top-level functions of
// this package whose results mention Config, Operation or vo.FilesAllowList.
var configProducers = []string{"Load", "LoadFrom"}

func TestOnlyLoadEntryPointsProduceAConfig(t *testing.T) {
	files := configNonTestFiles(t)
	if v := configProducerViolations(files); len(v) > 0 {
		t.Errorf("%s", strings.Join(v, "\n"))
	}
	var found []string
	for _, f := range files {
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.IsExported() && resultsMentionConfig(f, fn) {
				found = append(found, fn.Name.Name)
			}
		}
	}
	slices.Sort(found)
	want := slices.Clone(configProducers)
	slices.Sort(want)
	if !slices.Equal(found, want) {
		t.Errorf("exported Config producers are %q, want exactly %q", found, want)
	}
}

func TestFilesAllowListAccessorsTakeNoArgument(t *testing.T) {
	if v := filesAccessorViolations(configNonTestFiles(t)); len(v) > 0 {
		t.Errorf("%s", strings.Join(v, "\n"))
	}
}

// TestConfigGuardsCatchViolations feeds each checker source that breaks its
// rule, so a checker that stopped looking fails here.
func TestConfigGuardsCatchViolations(t *testing.T) {
	for _, tc := range []struct {
		name  string
		src   string
		check func([]*ast.File) []string
	}{
		{"a Config from bytes", `package config
func Parse(data []byte) (Config, error) { return Config{}, nil }`, configProducerViolations},
		{"a Config from a reader", `package config
import "io"
func FromReader(r io.Reader) (*Config, error) { return nil, nil }`, configProducerViolations},
		{"an Operation from a string", `package config
func FilesOperation(yaml string) Operation { return Operation{} }`, configProducerViolations},
		{"a list wrapped by a constructor", `package config
import "github.com/youyo/focal/internal/vo"
func AllowList(entries []string) vo.FilesAllowList { l, _ := vo.ParseFilesAllowList(entries); return l }`, configProducerViolations},
		{"Load that skips the permission check", `package config
func Load() (Config, error) { return build(rawConfig{}) }`, configProducerViolations},
		{"a method on Operation that returns one", `package config
func (o Operation) With(x int) Operation { return o }`, configProducerViolations},
		{"an accessor with an argument", `package config
import "github.com/youyo/focal/internal/vo"
func (o Operation) FilesAllowListFor(path string) vo.FilesAllowList { return o.filesPaths }`, filesAccessorViolations},
		{"an accessor under another import name", `package config
import v "github.com/youyo/focal/internal/vo"
func (c Config) Paths(extra []string) v.FilesAllowList { return v.FilesAllowList{} }`, filesAccessorViolations},
		{"a list returned through a pointer", `package config
import "github.com/youyo/focal/internal/vo"
func ListFrom(entries []string) *vo.FilesAllowList { return nil }`, filesAccessorViolations},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := parser.ParseFile(token.NewFileSet(), "violation.go", tc.src, parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if v := tc.check([]*ast.File{f}); len(v) == 0 {
				t.Errorf("checker accepted:\n%s", tc.src)
			}
		})
	}
}

// configProducerViolations reports an exported top-level function that can
// produce a Config, an Operation or a files list without being one of the
// reviewed entry points; an entry point that does not reach readConfigFile
// through loadFrom; and an exported method that returns a Config or an
// Operation from anything but a Config.
func configProducerViolations(files []*ast.File) []string {
	var out []string
	for _, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !fn.Name.IsExported() || !resultsMentionConfig(f, fn) {
				continue
			}
			if fn.Recv != nil {
				if recvName(fn) != "Config" && !resultsOnlyFilesList(f, fn) {
					out = append(out, fn.Name.Name+": an exported method on "+recvName(fn)+" returns a Config or an Operation")
				}
				continue
			}
			if !slices.Contains(configProducers, fn.Name.Name) {
				out = append(out, fn.Name.Name+": an exported function produces a Config, an Operation or a files list outside "+strings.Join(configProducers, " and "))
				continue
			}
			if !calls(fn.Body, "loadFrom") {
				out = append(out, fn.Name.Name+": does not read its file through loadFrom, which checks its permissions")
			}
		}
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == "loadFrom" && !calls(fn.Body, "readConfigFile") {
				out = append(out, "loadFrom: does not read the file through readConfigFile")
			}
		}
	}
	return out
}

// filesAccessorViolations reports an exported function or method whose results
// mention vo.FilesAllowList, under whatever name the file imports vo, and that
// takes an argument.
func filesAccessorViolations(files []*ast.File) []string {
	var out []string
	for _, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !fn.Name.IsExported() || !resultsMentionVo(f, fn, "FilesAllowList") {
				continue
			}
			if fn.Type.Params != nil && len(fn.Type.Params.List) > 0 {
				out = append(out, fn.Name.Name+": returns a vo.FilesAllowList and takes an argument")
			}
		}
	}
	return out
}

const voImportPath = "github.com/youyo/focal/internal/vo"

// voNames returns the names f refers to internal/vo by.
func voNames(f *ast.File) []string {
	var names []string
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil || p != voImportPath {
			continue
		}
		if imp.Name != nil {
			names = append(names, imp.Name.Name)
		} else {
			names = append(names, "vo")
		}
	}
	return names
}

func resultsMentionVo(f *ast.File, fn *ast.FuncDecl, typ string) bool {
	if fn.Type.Results == nil {
		return false
	}
	names := voNames(f)
	found := false
	for _, r := range fn.Type.Results.List {
		ast.Inspect(r.Type, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok && slices.Contains(names, id.Name) && sel.Sel.Name == typ {
					found = true
				}
			}
			return !found
		})
	}
	return found
}

func resultsMentionConfig(f *ast.File, fn *ast.FuncDecl) bool {
	if resultsMentionVo(f, fn, "FilesAllowList") {
		return true
	}
	if fn.Type.Results == nil {
		return false
	}
	found := false
	for _, r := range fn.Type.Results.List {
		ast.Inspect(r.Type, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.SelectorExpr:
				return false
			case *ast.Ident:
				found = found || v.Name == "Config" || v.Name == "Operation"
			}
			return !found
		})
	}
	return found
}

// resultsOnlyFilesList reports whether fn's results mention a files list but
// neither a Config nor an Operation: such a method is filesAccessorViolations'
// concern, not this check's.
func resultsOnlyFilesList(f *ast.File, fn *ast.FuncDecl) bool {
	if !resultsMentionVo(f, fn, "FilesAllowList") {
		return false
	}
	local := false
	for _, r := range fn.Type.Results.List {
		ast.Inspect(r.Type, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.SelectorExpr:
				return false
			case *ast.Ident:
				local = local || v.Name == "Config" || v.Name == "Operation"
			}
			return !local
		})
	}
	return !local
}

func recvName(fn *ast.FuncDecl) string {
	t := fn.Recv.List[0].Type
	if star, ok := t.(*ast.StarExpr); ok {
		t = star.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

func calls(body *ast.BlockStmt, name string) bool {
	if body == nil {
		return false
	}
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == name {
				found = true
			}
		}
		return !found
	})
	return found
}

func configNonTestFiles(t *testing.T) []*ast.File {
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
