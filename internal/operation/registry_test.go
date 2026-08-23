// registry_test.go exercises registryTable directly (unexported), the way
// exec_test.go tests buildPart/aggregate: the row set and the drift check
// against internal/policy are the part of the registry worth pinning down,
// and there is no way to reach registryTable from outside the package.
package operation

import (
	"slices"
	"testing"

	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/vo"
)

// TestRegistryMatchesPolicyCapabilities is the drift check: it reads
// internal/policy's declared operation names off the Allowed field of the
// error policy.Resolve returns for a name it does not know, and fails if
// that set and registryTable's key set are not identical. Adding an
// operation to only one of the two tables makes this fail.
func TestRegistryMatchesPolicyCapabilities(t *testing.T) {
	_, err := policy.Resolve("__not_a_real_operation__", policy.SudoNever)
	if err == nil {
		t.Fatal("policy.Resolve did not reject an unknown operation name")
	}
	want := slices.Clone(err.Allowed)
	slices.Sort(want)

	got := registryNames()

	if !slices.Equal(got, want) {
		t.Fatalf("registryTable names = %v, want %v (policy's declared capabilities)", got, want)
	}
}

func TestRegistryBuildUnknownOperation(t *testing.T) {
	r := NewRegistry(nil)
	_, err := r.Build("does-not-exist", Params{})
	if err == nil {
		t.Fatal("Build did not reject an unknown operation name")
	}
	if err.Code != "unknown_operation" {
		t.Errorf("Code = %q, want unknown_operation", err.Code)
	}
	if len(err.Allowed) != len(registryTable) {
		t.Errorf("Allowed has %d entries, want %d", len(err.Allowed), len(registryTable))
	}
}

// TestRegistryNoArgOperations checks the five fixed-command operations plus
// inspect build with no Params at all.
func TestRegistryNoArgOperations(t *testing.T) {
	r := NewRegistry(nil)
	for _, name := range []string{"system", "cpu", "memory", "storage", "network", "inspect"} {
		t.Run(name, func(t *testing.T) {
			op, err := r.Build(name, Params{})
			if err != nil {
				t.Fatalf("Build(%q): %v", name, err)
			}
			if op.Name() != name {
				t.Errorf("op.Name() = %q, want %q", op.Name(), name)
			}
		})
	}
}

// TestRegistryProcessesGoesThroughVOParse shows the processes row cannot
// hand a raw string to the operation: an invalid pid is rejected before a
// Processes value is ever built.
func TestRegistryProcessesGoesThroughVOParse(t *testing.T) {
	r := NewRegistry(nil)

	if _, err := r.Build("processes", Params{PID: "not-a-number"}); err == nil {
		t.Fatal("Build(processes) accepted an invalid pid")
	}
	if _, err := r.Build("processes", Params{ProcessName: "nginx"}); err != nil {
		t.Fatalf("Build(processes) with a valid name: %v", err)
	}
}

// TestRegistryServiceGoesThroughVOParse shows the service row rejects an
// invalid unit name (through NewService's own vo.ParseServiceName call)
// rather than passing it through.
func TestRegistryServiceGoesThroughVOParse(t *testing.T) {
	r := NewRegistry(nil)

	if _, err := r.Build("service", Params{ServiceName: "bad name with spaces"}); err == nil {
		t.Fatal("Build(service) accepted an invalid unit name")
	}
	if _, err := r.Build("service", Params{ServiceName: "nginx"}); err != nil {
		t.Fatalf("Build(service) with a valid name: %v", err)
	}
}

// TestRegistryLogsGoesThroughVOParse shows the logs row rejects an invalid
// since/lines and an invalid service name, and accepts an empty since/lines
// as "use the default".
func TestRegistryLogsGoesThroughVOParse(t *testing.T) {
	r := NewRegistry(nil)

	cases := []struct {
		name   string
		params Params
		wantOK bool
	}{
		{"missing service", Params{}, false},
		{"invalid service", Params{ServiceName: "bad name"}, false},
		{"invalid since", Params{ServiceName: "nginx", Since: "not-a-duration"}, false},
		{"invalid lines", Params{ServiceName: "nginx", Lines: "not-a-number"}, false},
		{"empty since and lines use defaults", Params{ServiceName: "nginx"}, true},
		{"explicit since and lines", Params{ServiceName: "nginx", Since: "15m", Lines: "50"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := r.Build("logs", tc.params)
			if tc.wantOK && err != nil {
				t.Fatalf("Build(logs, %+v): %v, want success", tc.params, err)
			}
			if !tc.wantOK && err == nil {
				t.Fatalf("Build(logs, %+v) succeeded, want rejection", tc.params)
			}
		})
	}
}

// TestRegistryKernelGoesThroughVOParse mirrors the logs case for kernel,
// which takes no service name.
func TestRegistryKernelGoesThroughVOParse(t *testing.T) {
	r := NewRegistry(nil)

	if _, err := r.Build("kernel", Params{Since: "bogus"}); err == nil {
		t.Fatal("Build(kernel) accepted an invalid since")
	}
	if _, err := r.Build("kernel", Params{}); err != nil {
		t.Fatalf("Build(kernel) with no params: %v", err)
	}
	if _, err := r.Build("kernel", Params{Since: "1h", Lines: "10"}); err != nil {
		t.Fatalf("Build(kernel) with explicit since/lines: %v", err)
	}
}

// TestRegistryLogsRespectsMaxSinceCeiling shows the row passes the
// administrator's ceiling through rather than dropping it: a since above
// MaxSince is refused, matching journalSince's reject-not-round rule.
func TestRegistryLogsRespectsMaxSinceCeiling(t *testing.T) {
	r := NewRegistry(nil)
	ceiling, err := vo.ParseDuration("1h")
	if err != nil {
		t.Fatalf("vo.ParseDuration: %v", err)
	}

	if _, buildErr := r.Build("logs", Params{ServiceName: "nginx", Since: "2h", MaxSince: ceiling}); buildErr == nil {
		t.Fatal("Build(logs) accepted a since above the configured max_since")
	}
	if _, buildErr := r.Build("logs", Params{ServiceName: "nginx", Since: "30m", MaxSince: ceiling}); buildErr != nil {
		t.Fatalf("Build(logs) within the ceiling: %v", buildErr)
	}
}

// TestRegistryInspectBuildsAnInspectThatSharesTheRegistry confirms the
// inspect row builds through NewInspect(r) rather than a hand-rolled
// alternative: the returned Operation is the concrete Inspect type inspect.go
// defines.
func TestRegistryInspectBuildsAnInspectThatSharesTheRegistry(t *testing.T) {
	r := NewRegistry(nil)
	op, err := r.Build("inspect", Params{})
	if err != nil {
		t.Fatalf("Build(inspect): %v", err)
	}
	if _, ok := op.(Inspect); !ok {
		t.Fatalf("Build(inspect) returned %T, want operation.Inspect", op)
	}
}
