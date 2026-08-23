package cli

import (
	"slices"
	"testing"

	"github.com/youyo/focal/internal/operation"
)

// TestOperationSpecs_MatchesFocalsOperations is the drift check on the CLI's
// dispatch table. The registry does not publish its own key set, so the
// expected names are written out here: adding an operation to focal without a
// row in operationSpecs — or leaving a row behind after removing one — fails
// here rather than silently making the operation unreachable from the CLI.
func TestOperationSpecs_MatchesFocalsOperations(t *testing.T) {
	want := []string{
		"cpu", "inspect", "kernel", "logs", "memory",
		"network", "processes", "service", "storage", "system",
	}
	if got := operationNames(); !slices.Equal(got, want) {
		t.Fatalf("operation names = %v, want %v", got, want)
	}
}

// TestOperationSpecs_AllBuild confirms every declared name is one the registry
// can actually build, so a row naming an operation focal does not implement is
// caught here rather than as an unknown_operation at runtime.
func TestOperationSpecs_AllBuild(t *testing.T) {
	registry := operation.NewRegistry(nil)
	for _, name := range operationNames() {
		t.Run(name, func(t *testing.T) {
			// Every operation that takes a caller value takes at most one
			// required one, and "nginx" is accepted for each of them.
			op, err := registry.Build(name, operation.Params{ServiceName: "nginx"})
			if err != nil {
				t.Fatalf("build %s: %v", name, err)
			}
			if op.Name() != name {
				t.Fatalf("built operation is named %q, want %q", op.Name(), name)
			}
		})
	}
}

func TestParseOperationArgs(t *testing.T) {
	cases := []struct {
		name   string
		op     string
		argv   []string
		want   operation.Params
		reject bool
	}{
		{
			name: "logs takes a positional unit and two flags",
			op:   "logs",
			argv: []string{"nginx", "--since", "30m", "--lines", "200"},
			want: operation.Params{ServiceName: "nginx", Since: "30m", Lines: "200"},
		},
		{
			name: "logs flags may precede the unit",
			op:   "logs",
			argv: []string{"--since", "30m", "nginx"},
			want: operation.Params{ServiceName: "nginx", Since: "30m"},
		},
		{
			name: "kernel takes no positional argument",
			op:   "kernel",
			argv: []string{"--since", "1h", "--lines", "50"},
			want: operation.Params{Since: "1h", Lines: "50"},
		},
		{
			name: "processes filters by name and pid",
			op:   "processes",
			argv: []string{"--name", "postgres", "--pid", "42"},
			want: operation.Params{ProcessName: "postgres", PID: "42"},
		},
		{
			name: "service takes the unit positionally",
			op:   "service",
			argv: []string{"nginx"},
			want: operation.Params{ServiceName: "nginx"},
		},
		{
			name: "system takes nothing",
			op:   "system",
			argv: nil,
			want: operation.Params{},
		},
		{
			name:   "a value that is not a flag focal declares is refused",
			op:     "logs",
			argv:   []string{"nginx", "--follow"},
			reject: true,
		},
		{
			name:   "an extra positional argument is refused",
			op:     "service",
			argv:   []string{"nginx", "sshd"},
			reject: true,
		},
		{
			name:   "a missing positional argument is refused",
			op:     "logs",
			argv:   nil,
			reject: true,
		},
		{
			name:   "an operation that takes nothing refuses an argument",
			op:     "system",
			argv:   []string{"extra"},
			reject: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseOperationArgs(tc.op, operationSpecs[tc.op], tc.argv)
			if tc.reject {
				if err == nil {
					t.Fatalf("params = %+v, want a usage error", got)
				}
				if err.code != exitUsage {
					t.Fatalf("exit code = %d, want %d", err.code, exitUsage)
				}
				if err.err.Message == "" {
					t.Fatal("the usage error carries no message")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("params = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestParseOperationArgs_PassesValuesThroughUntouched fixes that this layer
// decides nothing about a value: a rejected one still arrives at Params
// verbatim, so internal/vo remains the only place it is judged.
func TestParseOperationArgs_PassesValuesThroughUntouched(t *testing.T) {
	got, err := parseOperationArgs("logs", operationSpecs["logs"], []string{"nginx;id", "--since", "not-a-duration"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ServiceName != "nginx;id" || got.Since != "not-a-duration" {
		t.Fatalf("params = %+v, want the raw values", got)
	}
}

func TestOperationUsage(t *testing.T) {
	got := operationUsage("logs", operationSpecs["logs"])
	want := "focal HOST logs SERVICE [--since VALUE] [--lines VALUE]"
	if got != want {
		t.Fatalf("usage = %q, want %q", got, want)
	}
}
