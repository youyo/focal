package ssh

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"slices"
	"strings"
	"testing"
)

// allowedCommandFactories lists the exported functions this package may expose
// that hand a Command to another package. Every entry widens Focal's security
// boundary by one command, so the list is meant to be read as the complete
// catalogue of what a caller outside internal/ssh can ever run. Adding a
// factory to commands.go without adding it here fails the tests, which is the
// point: the catalogue is what a reviewer reads instead of the whole package.
var allowedCommandFactories = []string{
	"UptimeCommand",
}

// The asserts below read this package's own source with go/ast rather than
// exercising it through its API, because what has to hold is the absence of a
// construction path: no test can call a function that must not exist. Test
// files are excluded on purpose — they are not linkable from another package,
// so nothing declared in them can widen what the rest of Focal can reach.

func TestCommandHasNoExportedFields(t *testing.T) {
	spec := findTypeSpec(t, packageFiles(t), "Command")
	st, ok := spec.Type.(*ast.StructType)
	if !ok {
		t.Fatalf("Command must be a struct type, got %s", types.ExprString(spec.Type))
	}
	for _, f := range st.Fields.List {
		if len(f.Names) == 0 {
			t.Errorf("Command embeds %s: an embedded type exports its own surface through Command", types.ExprString(f.Type))
			continue
		}
		for _, name := range f.Names {
			if name.IsExported() {
				t.Errorf("Command has exported field %s: an exported field lets another package build a Command with a struct literal", name.Name)
			}
		}
	}
}

func TestOnlyAllowedExportedFunctionsReturnCommand(t *testing.T) {
	for _, fn := range funcDecls(packageFiles(t)) {
		if fn.Recv != nil || !fn.Name.IsExported() || !resultsMentionCommand(fn) {
			continue
		}
		if !slices.Contains(allowedCommandFactories, fn.Name.Name) {
			t.Errorf("exported function %s returns a Command but is not in allowedCommandFactories", fn.Name.Name)
		}
	}
}

func TestNewCommandIsTheUnexportedConstructor(t *testing.T) {
	var found *ast.FuncDecl
	for _, fn := range funcDecls(packageFiles(t)) {
		if fn.Recv != nil || !strings.EqualFold(fn.Name.Name, "newCommand") {
			continue
		}
		if fn.Name.IsExported() {
			t.Errorf("constructor %s is exported: only internal/ssh may build a Command", fn.Name.Name)
		}
		if fn.Name.Name == "newCommand" {
			found = fn
		}
	}
	if found == nil {
		t.Fatal("no unexported newCommand constructor found")
	}
	params := found.Type.Params.List
	if len(params) == 0 || !mentionsQualified(params[0].Type, "policy", "Policy") {
		t.Fatalf("newCommand must take a policy.Policy first, got %s", signature(found.Type.Params))
	}
	if got, want := signature(found.Type.Results), "Command, *result.Error"; got != want {
		t.Errorf("newCommand returns (%s), want (%s)", got, want)
	}
}

func TestExportedCommandFactoriesTakePolicy(t *testing.T) {
	for _, fn := range funcDecls(packageFiles(t)) {
		if fn.Recv != nil || !fn.Name.IsExported() || !resultsMentionCommand(fn) {
			continue
		}
		params := fn.Type.Params.List
		if len(params) == 0 || !mentionsQualified(params[0].Type, "policy", "Policy") {
			t.Errorf("exported factory %s must take a policy.Policy first so its sudo mode comes from policy.Resolve, got (%s)",
				fn.Name.Name, signature(fn.Type.Params))
		}
	}
}

func TestNoExportedEntryPointTakesSudoMode(t *testing.T) {
	for _, fn := range funcDecls(packageFiles(t)) {
		if !fn.Name.IsExported() {
			continue
		}
		for _, p := range fn.Type.Params.List {
			if mentionsQualified(p.Type, "policy", "SudoMode") {
				t.Errorf("exported %s takes a policy.SudoMode: a caller could then pick a sudo mode the capability table never approved",
					fn.Name.Name)
			}
		}
	}
}

func TestNoOtherExportedCommandSurface(t *testing.T) {
	files := packageFiles(t)
	for _, fn := range funcDecls(files) {
		if fn.Recv != nil && fn.Name.IsExported() && resultsMentionCommand(fn) {
			t.Errorf("exported method %s returns a Command outside the factory list", fn.Name.Name)
		}
	}
	for _, decl := range genDecls(files, token.VAR, token.CONST) {
		for _, spec := range decl.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			exposes := vs.Type != nil && mentionsCommand(vs.Type)
			for _, v := range vs.Values {
				exposes = exposes || mentionsCommand(v) || mentionsLocalIdent(v, "newCommand")
			}
			for _, name := range vs.Names {
				if name.IsExported() && exposes {
					t.Errorf("exported %s holds a Command: a package-level value hands out a Command without going through a factory", name.Name)
				}
			}
		}
	}
	for _, decl := range genDecls(files, token.TYPE) {
		for _, spec := range decl.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || !ts.Name.IsExported() || ts.Name.Name == "Command" {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				continue
			}
			for _, f := range st.Fields.List {
				if mentionsCommand(f.Type) {
					t.Errorf("exported type %s has a field of type %s: a Command must not travel inside another exported type",
						ts.Name.Name, types.ExprString(f.Type))
				}
			}
		}
	}
}

// packageFiles parses the non-test sources of this package.
func packageFiles(t *testing.T) []*ast.File {
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
		t.Fatal("no non-test sources found in the package directory")
	}
	return files
}

func funcDecls(files []*ast.File) []*ast.FuncDecl {
	var fns []*ast.FuncDecl
	for _, f := range files {
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok {
				fns = append(fns, fn)
			}
		}
	}
	return fns
}

func genDecls(files []*ast.File, kinds ...token.Token) []*ast.GenDecl {
	var decls []*ast.GenDecl
	for _, f := range files {
		for _, decl := range f.Decls {
			if gd, ok := decl.(*ast.GenDecl); ok && slices.Contains(kinds, gd.Tok) {
				decls = append(decls, gd)
			}
		}
	}
	return decls
}

func findTypeSpec(t *testing.T, files []*ast.File, name string) *ast.TypeSpec {
	t.Helper()
	for _, decl := range genDecls(files, token.TYPE) {
		for _, spec := range decl.Specs {
			if ts, ok := spec.(*ast.TypeSpec); ok && ts.Name.Name == name {
				return ts
			}
		}
	}
	t.Fatalf("type %s not declared in this package", name)
	return nil
}

func resultsMentionCommand(fn *ast.FuncDecl) bool {
	if fn.Type.Results == nil {
		return false
	}
	for _, r := range fn.Type.Results.List {
		if mentionsCommand(r.Type) {
			return true
		}
	}
	return false
}

// mentionsCommand reports whether an expression names this package's Command
// anywhere inside it, so *Command, []Command and map[string]Command are caught
// alongside a bare Command.
func mentionsCommand(expr ast.Expr) bool { return mentionsLocalIdent(expr, "Command") }

// mentionsLocalIdent looks for an unqualified identifier. A selector such as
// other.Command names a different package's type and is skipped along with
// everything under it.
func mentionsLocalIdent(expr ast.Expr, name string) bool {
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.SelectorExpr:
			return false
		case *ast.Ident:
			found = found || v.Name == name
		}
		return !found
	})
	return found
}

func mentionsQualified(expr ast.Expr, pkg, name string) bool {
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == pkg && sel.Sel.Name == name {
				found = true
			}
		}
		return !found
	})
	return found
}

// signature renders a parameter or result list the way it reads in source, for
// failure messages that name the offending signature.
func signature(fields *ast.FieldList) string {
	if fields == nil {
		return ""
	}
	var parts []string
	for _, f := range fields.List {
		rendered := types.ExprString(f.Type)
		if len(f.Names) == 0 {
			parts = append(parts, rendered)
			continue
		}
		for range f.Names {
			parts = append(parts, rendered)
		}
	}
	return strings.Join(parts, ", ")
}
