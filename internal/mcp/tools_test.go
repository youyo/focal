package mcp

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/youyo/focal/internal/operation"
	"github.com/youyo/focal/internal/result"
)

// TestPackageDoesNotImportSSH is the import boundary this package is built
// around: the MCP layer turns JSON into a typed Operation and hands it to a
// Runner, and knows nothing about how a host is reached. An import of
// internal/ssh here would mean a second place that could build a Command, so
// it is checked mechanically rather than left to review.
func TestPackageDoesNotImportSSH(t *testing.T) {
	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("list sources: %v", err)
	}
	if len(sources) == 0 {
		t.Fatal("no source files found to check")
	}

	fset := token.NewFileSet()
	var imports int
	for _, source := range sources {
		file, err := parser.ParseFile(fset, source, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", source, err)
		}
		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("%s: bad import %s", source, spec.Path.Value)
			}
			imports++
			if path == "github.com/youyo/focal/internal/ssh" {
				t.Errorf("%s imports internal/ssh; the MCP layer must reach it only through Runner", source)
			}
		}
	}
	// Guard the guard: the walk above is worthless if it never sees an
	// import at all.
	if imports == 0 {
		t.Fatal("the import walk found nothing to check")
	}
}

// TestToolNamesAreDerivedFromOperationNames fixes the one rule that maps an
// operation to the tool an agent calls, and that the mapping stays reversible:
// two operations may never collapse onto one tool name.
func TestToolNamesAreDerivedFromOperationNames(t *testing.T) {
	seen := map[string]string{}
	for _, name := range slices.Sorted(maps.Keys(toolSpecs)) {
		tool := toolName(name)
		if !strings.HasPrefix(tool, "inspect") {
			t.Errorf("operation %q maps to tool %q, want an inspect_ name", name, tool)
		}
		if other, dup := seen[tool]; dup {
			t.Errorf("operations %q and %q both map to tool %q", other, name, tool)
		}
		seen[tool] = name
	}
	if got := toolName("logs"); got != "inspect_logs" {
		t.Errorf("toolName(logs) = %q, want inspect_logs", got)
	}
	if got := toolName("inspect"); got != "inspect" {
		t.Errorf("toolName(inspect) = %q, want inspect", got)
	}
}

// TestToolSpecsCoverEveryOperation fails if focal grows an operation without a
// tool, or declares a tool for a name internal/operation cannot build.
func TestToolSpecsCoverEveryOperation(t *testing.T) {
	registry := operation.NewRegistry(nil)
	for name := range toolSpecs {
		if _, err := registry.Build(name, operation.Params{ServiceName: "nginx"}); err != nil {
			t.Errorf("operation %q has a tool but cannot be built: %v", name, err)
		}
	}
	for _, name := range []string{"system", "cpu", "memory", "storage", "network", "processes", "service", "logs", "kernel", "inspect"} {
		if _, ok := toolSpecs[name]; !ok {
			t.Errorf("operation %q has no tool", name)
		}
	}
}

// TestSchemasExposeOnlyTypedParameters is the security shape of this package
// stated as an assertion: what an agent may choose is a host, a login user, a
// registered key alias and the operation's own typed inputs. A property named
// anything else — a command, an argv, a path, an ssh option — would be a way
// to widen focal's surface from the wire, so the check is against a closed
// list rather than against a list of forbidden names.
func TestSchemasExposeOnlyTypedParameters(t *testing.T) {
	url, _ := serverFor(t, "operations:\n  kernel:\n    enabled: true\n", Options{})

	allowed := map[string][]string{
		"inspect":           {"host", "user", "identity", "transport"},
		"inspect_system":    {"host", "user", "identity", "transport"},
		"inspect_cpu":       {"host", "user", "identity", "transport"},
		"inspect_memory":    {"host", "user", "identity", "transport"},
		"inspect_storage":   {"host", "user", "identity", "transport"},
		"inspect_network":   {"host", "user", "identity", "transport"},
		"inspect_processes": {"host", "user", "identity", "transport", "name", "pid"},
		"inspect_service":   {"host", "user", "identity", "transport", "service"},
		"inspect_logs":      {"host", "user", "identity", "transport", "service", "since", "lines"},
		"inspect_kernel":    {"host", "user", "identity", "transport", "since", "lines"},
	}

	for name, schema := range toolSchemas(t, url) {
		want, known := allowed[name]
		if !known {
			t.Errorf("tool %q is not in the checked set", name)
			continue
		}
		got := slices.Sorted(maps.Keys(schema.Properties))
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("tool %q properties = %v, want %v", name, got, want)
		}
		if schema.AdditionalProperties != nil && *schema.AdditionalProperties {
			t.Errorf("tool %q accepts undeclared properties", name)
		}
		if !slices.Contains(schema.Required, "host") {
			t.Errorf("tool %q does not require a host", name)
		}
	}
}

type inputSchema struct {
	Properties           map[string]json.RawMessage `json:"properties"`
	Required             []string                   `json:"required"`
	AdditionalProperties *bool                      `json:"additionalProperties"`
}

func toolSchemas(t *testing.T, url string) map[string]inputSchema {
	t.Helper()
	resp := rpc(t, url, "tools/list", "", nil)
	if resp.Error != nil {
		t.Fatalf("tools/list: %+v", resp.Error)
	}
	var out struct {
		Tools []struct {
			Name        string      `json:"name"`
			Description string      `json:"description"`
			InputSchema inputSchema `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		t.Fatalf("decode tools: %v", err)
	}
	schemas := make(map[string]inputSchema, len(out.Tools))
	for _, tool := range out.Tools {
		if tool.Description == "" {
			t.Errorf("tool %q has no description", tool.Name)
		}
		schemas[tool.Name] = tool.InputSchema
	}
	return schemas
}

func TestArgsCarryTheirValuesIntoParams(t *testing.T) {
	pid, lines := 42, 200
	tests := []struct {
		name string
		args toolArgs
		want operation.Params
	}{
		{"none", hostOnlyArgs{}, operation.Params{}},
		{"service", serviceArgs{Service: "nginx"}, operation.Params{ServiceName: "nginx"}},
		{
			"processes",
			processesArgs{Name: "nginx", PID: &pid},
			operation.Params{ProcessName: "nginx", PID: "42"},
		},
		{
			"logs",
			logsArgs{Service: "nginx", Since: "30m", Lines: &lines},
			operation.Params{ServiceName: "nginx", Since: "30m", Lines: "200"},
		},
		{"kernel", kernelArgs{Since: "1h"}, operation.Params{Since: "1h"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.args.params(); got != tt.want {
				t.Errorf("params = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestResolveTargetLayersTheLoginUser(t *testing.T) {
	tests := []struct {
		name        string
		host        string
		user        string
		defaultUser string
		want        string
		wantErr     string
	}{
		{name: "bare host", host: "web", want: "web"},
		{name: "host names the user", host: "root@web", want: "root@web"},
		{name: "call overrides", host: "web", user: "ec2-user", want: "ec2-user@web"},
		{name: "server default", host: "web", defaultUser: "ec2-user", want: "ec2-user@web"},
		{
			name: "call beats server default", host: "web", user: "root",
			defaultUser: "ec2-user", want: "root@web",
		},
		{
			name: "host beats server default", host: "root@web",
			defaultUser: "ec2-user", want: "root@web",
		},
		{
			name: "host and call both name a user", host: "root@web", user: "ec2-user",
			wantErr: "conflicting_user",
		},
		{name: "injection in the user", host: "web", user: "root;id", wantErr: "invalid_target"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveTarget(tt.host, tt.user, tt.defaultUser)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("target = %q, want error %q", got.String(), tt.wantErr)
				}
				if err.Code != tt.wantErr {
					t.Errorf("code = %q, want %q", err.Code, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveTarget: %v", err)
			}
			if got.String() != tt.want {
				t.Errorf("target = %q, want %q", got.String(), tt.want)
			}
		})
	}
}

// TestProcessesArgsRejectInjection closes the MCP-boundary gap
// TestValidationErrorIsAToolErrorNotAProtocolError and
// TestHostIsNotAllowlistedButIsValidated leave open: those exercise the
// ServiceName and Target(host) parameters end-to-end through inspect_logs and
// inspect_system, but no test in this package had ever called inspect_processes
// with an injection payload in its name or pid arguments. ProcessName and PID
// are the two remaining reject types issue #15 names, and this tool is the
// only one that exposes either from the wire.
func TestProcessesArgsRejectInjection(t *testing.T) {
	url, runner := serverFor(t, "", Options{})

	cases := []struct {
		name string
		args map[string]any
	}{
		{"process name with a command separator", map[string]any{"host": "web", "name": "nginx;id"}},
		{"process name with a substitution", map[string]any{"host": "web", "name": "$(id)"}},
		{"process name with a traversal", map[string]any{"host": "web", "name": "../../etc/passwd"}},
		{"process name with embedded whitespace", map[string]any{"host": "web", "name": "nginx id"}},
		{"pid above its own bound", map[string]any{"host": "web", "pid": 4194305}},
		{"pid as a negative number", map[string]any{"host": "web", "pid": -1000}},
		{"pid as zero", map[string]any{"host": "web", "pid": 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := callTool(t, url, "inspect_processes", tc.args)
			if !res.IsError {
				t.Fatalf("%+v was accepted", tc.args)
			}
			got := toolErrorOf(t, res)
			if got.Kind != result.KindValidation {
				t.Errorf("kind = %q, want %q", got.Kind, result.KindValidation)
			}
			if len(runner.calls) != 0 {
				t.Errorf("a rejected input still reached the runner: %+v", runner.calls)
			}
		})
	}
}
