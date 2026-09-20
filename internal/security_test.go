// Package internal_test is a cross-package security regression test. It is
// deliberately independent of internal/ssh's own test suite: boundary_test.go
// and commands_test.go already tie a hand-maintained factory table to the
// exported surface of internal/ssh/commands.go, but a change made entirely
// inside that package (to the table, to its shared test helpers, or to both
// at once) could weaken the boundary and the tests that watch it together.
// This file rediscovers the factory catalogue from the outside, with its own
// go/ast walk over commands.go, and its own hardcoded call table — so a new
// exported command factory has to be added here too, with realistic inputs,
// before its argv is ever exercised by anything under internal_test.
package internal_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/ssh"
	"github.com/youyo/focal/internal/vo"
)

// exportedCommandFactories parses internal/ssh/commands.go directly (not
// through go/types) and lists every top-level, exported function whose result
// list mentions the package-local Command type. That is the same shape
// boundary_test.go's own TestOnlyAllowedExportedFunctionsReturnCommand
// checks, run again here against a table this file owns.
func exportedCommandFactories(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "ssh/commands.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse internal/ssh/commands.go: %v", err)
	}
	var names []string
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || !fn.Name.IsExported() || fn.Type.Results == nil {
			continue
		}
		for _, r := range fn.Type.Results.List {
			if mentionsUnqualifiedCommand(r.Type) {
				names = append(names, fn.Name.Name)
				break
			}
		}
	}
	if len(names) == 0 {
		t.Fatal("found no exported command factory in ssh/commands.go; the go/ast walk is broken")
	}
	return names
}

func mentionsUnqualifiedCommand(expr ast.Expr) bool {
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.SelectorExpr:
			return false
		case *ast.Ident:
			found = found || v.Name == "Command"
		}
		return !found
	})
	return found
}

// securityFactoryCall is one exported factory built with realistic accepted
// inputs. callerTokens names the exact strings, among the resulting Program
// and Args, that came from a value object rather than from a literal Focal
// wrote itself — the set a future PR could widen without touching this file
// only by adding a new factory (caught by TestFactoryCatalogueHasNoDrift) or
// by changing what an existing vo type accepts (caught by the vo package's
// own reject tests, and belt-and-suspenders here too).
type securityFactoryCall struct {
	name         string
	build        func(t *testing.T) ssh.Command
	callerTokens []string
}

func mustServiceNameForSecurityTest(t *testing.T, s string) vo.ServiceName {
	t.Helper()
	v, err := vo.ParseServiceName(s)
	if err != nil {
		t.Fatalf("vo.ParseServiceName(%q): %v", s, err)
	}
	return v
}

func mustDurationForSecurityTest(t *testing.T, s string) vo.Duration {
	t.Helper()
	v, err := vo.ParseDuration(s)
	if err != nil {
		t.Fatalf("vo.ParseDuration(%q): %v", s, err)
	}
	return v
}

func mustLineLimitForSecurityTest(t *testing.T, s string) vo.LineLimit {
	t.Helper()
	v, err := vo.ParseLineLimit(s)
	if err != nil {
		t.Fatalf("vo.ParseLineLimit(%q): %v", s, err)
	}
	return v
}

// buildOrFail returns a closure so a factory's two-valued call can be passed
// straight through as its sole argument (must(ssh.UptimeCommand(p))), the way
// internal/ssh/commands_test.go's own commandBuilder does.
func buildOrFail(t *testing.T) func(ssh.Command, *result.Error) ssh.Command {
	t.Helper()
	return func(cmd ssh.Command, err *result.Error) ssh.Command {
		t.Helper()
		if err != nil {
			t.Fatalf("factory rejected its own accepted arguments: %v", err)
		}
		return cmd
	}
}

// securityFactoryCalls is the hardcoded table TestFactoryCatalogueHasNoDrift
// checks against exportedCommandFactories, and the table
// TestFactoryArgvCarriesNoDangerousBytes exercises. It is written by hand, on
// purpose: nothing here is derived from commands.go or from internal/ssh's own
// test tables, so it cannot silently track a change made only in that package.
func securityFactoryCalls(t *testing.T) []securityFactoryCall {
	t.Helper()
	p := policy.Policy{}
	unit := mustServiceNameForSecurityTest(t, "nginx.service")
	since := mustDurationForSecurityTest(t, "30m")
	kernelSince := mustDurationForSecurityTest(t, "1h")
	lines := mustLineLimitForSecurityTest(t, "200")

	return []securityFactoryCall{
		{"UptimeCommand", func(t *testing.T) ssh.Command { return buildOrFail(t)(ssh.UptimeCommand(p)) }, nil},
		{"UnameCommand", func(t *testing.T) ssh.Command { return buildOrFail(t)(ssh.UnameCommand(p)) }, nil},
		{"HostnameCommand", func(t *testing.T) ssh.Command { return buildOrFail(t)(ssh.HostnameCommand(p)) }, nil},
		{"DateCommand", func(t *testing.T) ssh.Command { return buildOrFail(t)(ssh.DateCommand(p)) }, nil},
		{"OSReleaseCommand", func(t *testing.T) ssh.Command { return buildOrFail(t)(ssh.OSReleaseCommand(p)) }, nil},
		{"LSCPUCommand", func(t *testing.T) ssh.Command { return buildOrFail(t)(ssh.LSCPUCommand(p)) }, nil},
		{"LoadAvgCommand", func(t *testing.T) ssh.Command { return buildOrFail(t)(ssh.LoadAvgCommand(p)) }, nil},
		{"FreeCommand", func(t *testing.T) ssh.Command { return buildOrFail(t)(ssh.FreeCommand(p)) }, nil},
		{"MemInfoCommand", func(t *testing.T) ssh.Command { return buildOrFail(t)(ssh.MemInfoCommand(p)) }, nil},
		{"DFCommand", func(t *testing.T) ssh.Command { return buildOrFail(t)(ssh.DFCommand(p)) }, nil},
		{"LSBLKCommand", func(t *testing.T) ssh.Command { return buildOrFail(t)(ssh.LSBLKCommand(p)) }, nil},
		{"FindMntCommand", func(t *testing.T) ssh.Command { return buildOrFail(t)(ssh.FindMntCommand(p)) }, nil},
		{"IPAddrCommand", func(t *testing.T) ssh.Command { return buildOrFail(t)(ssh.IPAddrCommand(p)) }, nil},
		{"IPRouteCommand", func(t *testing.T) ssh.Command { return buildOrFail(t)(ssh.IPRouteCommand(p)) }, nil},
		{"SocketStatsCommand", func(t *testing.T) ssh.Command { return buildOrFail(t)(ssh.SocketStatsCommand(p)) }, nil},
		{"ResolvConfCommand", func(t *testing.T) ssh.Command { return buildOrFail(t)(ssh.ResolvConfCommand(p)) }, nil},
		{"ProcessListCommand", func(t *testing.T) ssh.Command { return buildOrFail(t)(ssh.ProcessListCommand(p)) }, nil},
		{
			"ServiceStatusCommand",
			func(t *testing.T) ssh.Command { return buildOrFail(t)(ssh.ServiceStatusCommand(p, unit)) },
			[]string{"nginx.service"},
		},
		{
			"LogsCommand",
			func(t *testing.T) ssh.Command { return buildOrFail(t)(ssh.LogsCommand(p, unit, since, lines)) },
			[]string{"nginx.service", "200"},
		},
		{
			"KernelLogsCommand",
			func(t *testing.T) ssh.Command { return buildOrFail(t)(ssh.KernelLogsCommand(p, kernelSince, lines)) },
			[]string{"200"},
		},
	}
}

// TestFactoryCatalogueHasNoDrift is the set-equality half of the regression:
// every exported function commands.go declares that can hand out a Command
// must be exercised below, and nothing below may name a factory commands.go
// does not export. A factory added to one side without the other fails here,
// independently of allowedCommandFactories and factoryCases in internal/ssh.
func TestFactoryCatalogueHasNoDrift(t *testing.T) {
	discovered := exportedCommandFactories(t)
	sort.Strings(discovered)

	var covered []string
	for _, fc := range securityFactoryCalls(t) {
		covered = append(covered, fc.name)
	}
	sort.Strings(covered)

	if !slices.Equal(discovered, covered) {
		t.Errorf("go/ast finds exported command factories\n %q\nsecurityFactoryCalls covers\n %q",
			discovered, covered)
	}
}

// safeArgvBytes is the complete set an argv token may contain: letters,
// digits, and the punctuation Focal's own command literals and vo-typed
// values legitimately need (paths, key=value pairs, timestamps, service unit
// suffixes). Everything outside it — shell metacharacters (; | & $ ` ( ) < >
// \ ' " * ? { } ~ #), whitespace, and control bytes including NUL — gives a
// shell or a program a way to read one token as more than inert data, so none
// of it may ever appear in what Focal sends, whether the byte came from a
// literal Focal wrote or from a value a caller supplied.
const safeArgvBytes = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_@+=:,./%-"

func isDangerousArgvByte(b byte) bool {
	for i := 0; i < len(safeArgvBytes); i++ {
		if safeArgvBytes[i] == b {
			return false
		}
	}
	return true
}

// TestFactoryArgvCarriesNoDangerousBytes runs every factory the go/ast walk
// found and checks the argv it actually produced, byte by byte, against the
// property issue #15/#16 exist to protect: a shell metacharacter, embedded
// whitespace or a control character in one token would be the first sign that
// something upstream stopped treating this boundary as a value object, and a
// caller-supplied token starting with a hyphen would be read as an option by
// ssh or by the remote program rather than as data.
func TestFactoryArgvCarriesNoDangerousBytes(t *testing.T) {
	for _, fc := range securityFactoryCalls(t) {
		t.Run(fc.name, func(t *testing.T) {
			cmd := fc.build(t)
			tokens := append([]string{cmd.Program()}, cmd.Args()...)
			for _, tok := range tokens {
				for i := 0; i < len(tok); i++ {
					if isDangerousArgvByte(tok[i]) {
						t.Errorf("token %q carries a byte at index %d (%q) that no argv token may carry", tok, i, tok[i:i+1])
					}
				}
				if slices.Contains(fc.callerTokens, tok) && len(tok) > 0 && tok[0] == '-' {
					t.Errorf("caller-supplied token %q starts with a hyphen: it would be read as an option", tok)
				}
			}
		})
	}
}

// ssmNonTestGoFiles lists internal/ssm's own source files, excluding tests,
// the same way exportedCommandFactories reads ssh/commands.go directly rather
// than through go/types: this file's whole purpose is to rediscover a
// property from outside the package it is about, independently of anything
// internal/ssm/boundary_test.go asserts about itself.
func ssmNonTestGoFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir("ssm")
	if err != nil {
		t.Fatalf("read internal/ssm: %v", err)
	}
	var files []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		files = append(files, "ssm/"+name)
	}
	if len(files) == 0 {
		t.Fatal("found no non-test .go file in internal/ssm; the walk is broken")
	}
	return files
}

// TestSSMExportedFunctionsTakeNoRawString is internal/ssm's counterpart to
// TestOnlyAllowedExportedFunctionsReturnCommand in internal/ssh/boundary_test.go:
// that test keeps internal/ssh's Command from being constructible outside a
// reviewed factory, and this one keeps internal/ssm from having a second,
// looser entry point that accepted a bare string and built a shell line from
// it instead of from an already-validated ssh.Command. Run from outside the
// package with its own go/ast walk, so a change made only inside internal/ssm
// cannot silence it.
func TestSSMExportedFunctionsTakeNoRawString(t *testing.T) {
	fset := token.NewFileSet()
	checked := 0
	for _, path := range ssmNonTestGoFiles(t) {
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !fn.Name.IsExported() || fn.Type.Params == nil {
				continue
			}
			checked++
			for _, param := range fn.Type.Params.List {
				if ident, ok := param.Type.(*ast.Ident); ok && ident.Name == "string" {
					t.Errorf("%s.%s takes a bare string parameter; every command internal/ssm runs must arrive as an ssh.Command",
						path, fn.Name.Name)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("found no exported function in internal/ssm to check; the walk is broken")
	}
}

// TestSSMSendCommandBuildsCommandsFromCommandLineOnly reads internal/ssm's own
// executor.go and asserts that the "commands" element of the SendCommand
// request is built from exactly one call to commandLine — the single
// unexported function that turns a validated ssh.Command into the shell line
// AWS-RunShellScript runs — rather than from any string literal or
// concatenation a later change could add beside it.
func TestSSMSendCommandBuildsCommandsFromCommandLineOnly(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "ssm/executor.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse ssm/executor.go: %v", err)
	}

	found := 0
	ast.Inspect(file, func(n ast.Node) bool {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "Parameters" {
			return true
		}
		mapLit, ok := kv.Value.(*ast.CompositeLit)
		if !ok {
			t.Fatalf("SendCommandInput.Parameters is not a composite literal: %T", kv.Value)
		}
		for _, elt := range mapLit.Elts {
			pair, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			lit, ok := pair.Key.(*ast.BasicLit)
			if !ok || lit.Value != `"commands"` {
				continue
			}
			found++
			sliceLit, ok := pair.Value.(*ast.CompositeLit)
			if !ok || len(sliceLit.Elts) != 1 {
				t.Fatalf(`Parameters["commands"] is not a one-element slice literal: %#v`, pair.Value)
			}
			call, ok := sliceLit.Elts[0].(*ast.CallExpr)
			if !ok {
				t.Fatalf(`Parameters["commands"][0] is not a function call: %#v`, sliceLit.Elts[0])
			}
			fnIdent, ok := call.Fun.(*ast.Ident)
			if !ok || fnIdent.Name != "commandLine" {
				t.Errorf(`Parameters["commands"][0] calls %v, want a call to commandLine`, call.Fun)
			}
		}
		return true
	})
	if found == 0 {
		t.Fatal(`found no SendCommandInput.Parameters["commands"] in ssm/executor.go; the walk is broken`)
	}
}
