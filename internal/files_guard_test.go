package internal_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// This file holds, across the whole module, the properties ADR 0012 rests on
// that no type can state by itself:
//
//   - the files allow list is built only by internal/config: nothing else
//     refers to vo.ParseFilesAllowList, under any import name, and nothing
//     dot-imports internal/vo, which would hide such a reference;
//   - the files operation's types travel only where they are needed: only the
//     files operation (and the Params it is built from), its factory,
//     internal/config and internal/vo itself name vo.FilesPath,
//     vo.FilesAllowList or their parsers, and no file outside internal/vo
//     declares a type alias or a defined type on top of them. CLI and MCP
//     carry the configured list from config to Params without naming it;
//   - no non-test code imports unsafe, which is the one way to fill in a
//     guarded type's unexported field from another package.
//
// reflect is not banned: it cannot set an unexported field without unsafe.

const voPath = "github.com/youyo/focal/internal/vo"

// filesGuardedNames are internal/vo's identifiers the second rule is about.
var filesGuardedNames = []string{"FilesPath", "FilesAllowList", "ParseFilesPath", "ParseFilesAllowList"}

// filesReferenceAllowed lists the files, relative to internal/, that may name
// filesGuardedNames, and the packages (a trailing "/") all of whose files may.
var filesReferenceAllowed = []string{
	"vo/",
	"config/",
	"ssh/commands.go",
	"operation/files.go",
	"operation/registry.go",
}

// allowListParserAllowed is where vo.ParseFilesAllowList may be named.
var allowListParserAllowed = []string{"config/"}

func TestFilesGuardsHoldAcrossTheModule(t *testing.T) {
	files := moduleNonTestFiles(t)
	if v := filesReferenceViolations(files); len(v) > 0 {
		t.Errorf("%s", strings.Join(v, "\n"))
	}
	// The walk must have seen the places the rules are about, or a broken
	// walk would pass everything.
	for _, want := range []string{"vo/files.go", "config/config.go", "ssh/commands.go", "operation/files.go", "cli/root.go", "mcp/server.go", "../cmd/focal/main.go"} {
		if _, ok := files[want]; !ok {
			t.Errorf("the module walk did not reach %s", want)
		}
	}
}

func TestFilesGuardsCatchViolations(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		src  string
	}{
		{"cli builds a list", "cli/root.go", `package cli
import "github.com/youyo/focal/internal/vo"
func list(xs []string) { _, _ = vo.ParseFilesAllowList(xs) }`},
		{"a function value under another name", "mcp/server.go", `package mcp
import v "github.com/youyo/focal/internal/vo"
var parse = v.ParseFilesAllowList`},
		{"the operation builds its own list", "operation/files.go", `package operation
import "github.com/youyo/focal/internal/vo"
func widen() { _, _ = vo.ParseFilesAllowList([]string{"/"}) }`},
		{"vo itself wraps the parser", "vo/other.go", `package vo
func everything() { _, _ = ParseFilesAllowList([]string{"/"}) }`},
		{"a dot import of vo", "cli/root.go", `package cli
import . "github.com/youyo/focal/internal/vo"
var _ = ParseTarget`},
		{"another operation takes a FilesPath", "operation/logs.go", `package operation
import "github.com/youyo/focal/internal/vo"
type readFile struct{ p vo.FilesPath }`},
		{"mcp names the list type", "mcp/tools.go", `package mcp
import "github.com/youyo/focal/internal/vo"
var _ vo.FilesAllowList`},
		{"another factory file", "ssh/read.go", `package ssh
import "github.com/youyo/focal/internal/vo"
func cat(p vo.FilesPath) {}`},
		{"an alias that renames the type", "operation/files.go", `package operation
import "github.com/youyo/focal/internal/vo"
type Dir = vo.FilesPath`},
		{"a defined type on top of the list", "config/config.go", `package config
import "github.com/youyo/focal/internal/vo"
type Paths vo.FilesAllowList`},
		{"unsafe", "cli/root.go", `package cli
import "unsafe"
var _ = unsafe.Sizeof(0)`},
		{"unsafe in cmd", "../cmd/focal/main.go", `package main
import _ "unsafe"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := parser.ParseFile(token.NewFileSet(), tc.path, tc.src, parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if v := filesReferenceViolations(map[string]*ast.File{tc.path: f}); len(v) == 0 {
				t.Errorf("checker accepted %s:\n%s", tc.path, tc.src)
			}
		})
	}
}

// TestFilesGuardsAcceptTheIntendedShape keeps the checker from being so broad
// that the real code could not be written at all.
func TestFilesGuardsAcceptTheIntendedShape(t *testing.T) {
	for _, tc := range []struct {
		path string
		src  string
	}{
		{"config/config.go", `package config
import "github.com/youyo/focal/internal/vo"
func paths(xs []string) (vo.FilesAllowList, error) { l, _ := vo.ParseFilesAllowList(xs); return l, nil }`},
		{"operation/registry.go", `package operation
import "github.com/youyo/focal/internal/vo"
type Params struct{ FilesAllowList vo.FilesAllowList }
func build(p Params) { _, _ = vo.ParseFilesPath("/x", p.FilesAllowList) }`},
		{"cli/root.go", `package cli
func carry(params, op interface{}) {}`},
		{"vo/files.go", `package vo
func ParseFilesAllowList(xs []string) (FilesAllowList, error) { return FilesAllowList{}, nil }`},
	} {
		f, err := parser.ParseFile(token.NewFileSet(), tc.path, tc.src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if v := filesReferenceViolations(map[string]*ast.File{tc.path: f}); len(v) > 0 {
			t.Errorf("checker refused %s: %v", tc.path, v)
		}
	}
}

func filesReferenceViolations(files map[string]*ast.File) []string {
	var out []string
	keys := mapsKeys(files)
	slices.Sort(keys)
	for _, path := range keys {
		f := files[path]
		inVo := strings.HasPrefix(path, "vo/")
		var voNames []string
		for _, imp := range f.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				continue
			}
			if p == "unsafe" {
				out = append(out, path+": imports unsafe")
			}
			if p != voPath {
				continue
			}
			switch {
			case imp.Name == nil:
				voNames = append(voNames, "vo")
			case imp.Name.Name == ".":
				out = append(out, path+": dot-imports internal/vo")
			case imp.Name.Name != "_":
				voNames = append(voNames, imp.Name.Name)
			}
		}

		// named reports whether expr is a reference to one of internal/vo's
		// guarded identifiers, and which.
		named := func(expr ast.Node) string {
			switch v := expr.(type) {
			case *ast.SelectorExpr:
				if id, ok := v.X.(*ast.Ident); ok && slices.Contains(voNames, id.Name) && slices.Contains(filesGuardedNames, v.Sel.Name) {
					return v.Sel.Name
				}
			case *ast.Ident:
				if inVo && slices.Contains(filesGuardedNames, v.Name) {
					return v.Name
				}
			}
			return ""
		}

		var declNames []*ast.Ident
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok {
				declNames = append(declNames, fn.Name)
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			name := named(n)
			if name == "" {
				return true
			}
			if id, ok := n.(*ast.Ident); ok && slices.Contains(declNames, id) {
				return true // the declaration itself, in internal/vo
			}
			if name == "ParseFilesAllowList" && !allowedAt(path, allowListParserAllowed) {
				out = append(out, path+": refers to vo.ParseFilesAllowList; only internal/config builds the files allow list")
			}
			if !allowedAt(path, filesReferenceAllowed) {
				out = append(out, path+": refers to vo."+name+" outside the files operation, its factory, internal/config and internal/vo")
			}
			return false
		})

		if inVo {
			continue
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts := spec.(*ast.TypeSpec)
				if name := named(ts.Type); name != "" {
					out = append(out, path+": declares "+ts.Name.Name+" on top of vo."+name)
				}
			}
		}
	}
	return out
}

func allowedAt(path string, allowed []string) bool {
	for _, a := range allowed {
		if strings.HasSuffix(a, "/") && strings.HasPrefix(path, a) || path == a {
			return true
		}
	}
	return false
}

func mapsKeys(m map[string]*ast.File) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// moduleNonTestFiles parses every non-test Go file under internal/ and cmd/,
// keyed by its path relative to internal/.
func moduleNonTestFiles(t *testing.T) map[string]*ast.File {
	t.Helper()
	fset := token.NewFileSet()
	files := make(map[string]*ast.File)
	for _, root := range []string{".", "../cmd"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, parseErr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if parseErr != nil {
				return parseErr
			}
			files[filepath.ToSlash(path)] = f
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	return files
}
