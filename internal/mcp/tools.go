package mcp

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/youyo/focal/internal/operation"
	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/vo"
)

// This file is the MCP layer's whole knowledge of what an operation takes. Each
// operation is one row in toolSpecs pairing a summary with the Go type its
// arguments arrive as, and that type is the only description of the tool's
// input schema: the SDK infers the JSON Schema from it, validates incoming
// arguments against it, and rejects anything the type does not declare.
//
// What an agent can choose is fixed by those types and nothing else. There is
// no property for a command, an argv, a file path or an ssh_config option, and
// no row that passes a value through untyped — every caller-supplied string
// lands in operation.Params and reaches an operation through internal/vo's
// Parse*, exactly as the CLI's own arguments do.

// hostArgs is the part of every tool's input that says where to look and how
// to get there. It is embedded in each operation's argument type, so the three
// properties are declared once and appear on every tool.
type hostArgs struct {
	Host string `json:"host" jsonschema:"the host to inspect, named exactly as ssh(1) takes it: an ssh_config alias, a hostname, an IP address, optionally with a leading user@; when transport is ssm, an EC2 instance ID such as i-0123456789abcdef0 instead"`
	User string `json:"user,omitempty" jsonschema:"the user to log in as, when the host does not name one; not accepted when transport is ssm"`
	// Identity is an alias, never a path: the aliases were registered when
	// focal serve was started, so an agent can choose among the keys the
	// operator offered without being able to name a file on the server.
	Identity string `json:"identity,omitempty" jsonschema:"the alias of a private key registered with focal serve --identity; key file paths are not accepted; not accepted when transport is ssm"`
	// Transport chooses how focal reaches Host. It defaults to ssh so every
	// existing call keeps meaning what it always has; an installation must
	// opt in to ssm through execution.transports before a call naming it
	// succeeds.
	Transport string `json:"transport,omitempty" jsonschema:"how to reach host: ssh (default) or ssm; ssm means AWS SSM Run Command, and host must then be an EC2 instance ID"`
}

func (h hostArgs) destination() hostArgs { return h }

// toolArgs is what every tool's argument type provides: where the operation
// runs, and the operation's own typed inputs as the raw text internal/vo will
// be asked to accept.
type toolArgs interface {
	destination() hostArgs
	params() operation.Params
}

type hostOnlyArgs struct {
	hostArgs
}

func (hostOnlyArgs) params() operation.Params { return operation.Params{} }

type serviceArgs struct {
	hostArgs
	Service string `json:"service" jsonschema:"the systemd unit to report on, e.g. nginx or nginx.service"`
}

func (a serviceArgs) params() operation.Params {
	return operation.Params{ServiceName: a.Service}
}

type processesArgs struct {
	hostArgs
	Name string `json:"name,omitempty" jsonschema:"only processes whose command name contains this text"`
	PID  *int   `json:"pid,omitempty" jsonschema:"only the process with this pid"`
}

func (a processesArgs) params() operation.Params {
	return operation.Params{ProcessName: a.Name, PID: itoa(a.PID)}
}

type logsArgs struct {
	hostArgs
	Service string `json:"service" jsonschema:"the systemd unit whose journal to read, e.g. nginx or nginx.service"`
	Since   string `json:"since,omitempty" jsonschema:"how far back to read, e.g. 30m or 24h"`
	Lines   *int   `json:"lines,omitempty" jsonschema:"how many lines to return, e.g. 200"`
}

func (a logsArgs) params() operation.Params {
	return operation.Params{ServiceName: a.Service, Since: a.Since, Lines: itoa(a.Lines)}
}

type kernelArgs struct {
	hostArgs
	Since string `json:"since,omitempty" jsonschema:"how far back to read, e.g. 1h or 24h"`
	Lines *int   `json:"lines,omitempty" jsonschema:"how many lines to return, e.g. 200"`
}

func (a kernelArgs) params() operation.Params {
	return operation.Params{Since: a.Since, Lines: itoa(a.Lines)}
}

// itoa renders an optional integer as the raw text internal/vo parses. The
// pointer is what distinguishes "not named" from a value: an absent parameter
// becomes the empty string, which internal/operation reads as "apply the
// default", while a named 0 or a negative count is carried across and refused
// by the value object rather than silently turned into a default.
func itoa(v *int) string {
	if v == nil {
		return ""
	}
	return strconv.Itoa(*v)
}

// toolSpec is one row: the summary an agent reads and the function that
// registers the tool with its argument type. The type parameter cannot be
// stored in a struct field, so each row carries an instantiation of add
// instead, which is what binds an operation to the schema of its arguments.
type toolSpec struct {
	summary string
	add     func(srv *sdk.Server, s *server, name, summary string)
}

// toolSpecs declares one tool per operation focal implements. A name absent
// here has no tool, and a name present here must be buildable by
// internal/operation's registry — tools_test.go fails on either drift.
var toolSpecs = map[string]toolSpec{
	"system":    {"Report the host's identity, kernel, uptime and load.", add[hostOnlyArgs]},
	"cpu":       {"Report the host's processor model, core count and current load.", add[hostOnlyArgs]},
	"memory":    {"Report the host's memory and swap usage.", add[hostOnlyArgs]},
	"storage":   {"Report the host's filesystem usage and mounted block devices.", add[hostOnlyArgs]},
	"network":   {"Report the host's interfaces, addresses and listening sockets.", add[hostOnlyArgs]},
	"processes": {"List the host's running processes, optionally narrowed to one name or pid.", add[processesArgs]},
	"service":   {"Report the state of one systemd unit.", add[serviceArgs]},
	"logs":      {"Read one systemd unit's journal.", add[logsArgs]},
	"kernel":    {"Read the kernel's own journal.", add[kernelArgs]},
	"inspect":   {"Run focal's whole read-only survey of a host in one call.", add[hostOnlyArgs]},
}

// toolName maps an operation name to the tool an agent calls. Every operation
// is prefixed with inspect_ so that focal's tools read as one family in a tool
// list and can be authorised as one group by a gateway in front of the server.
// The operation named "inspect" is the mapping's one fixed point: naming it
// twice would say nothing the prefix does not already say.
func toolName(operation string) string {
	if operation == "inspect" {
		return operation
	}
	return "inspect_" + operation
}

// add registers one operation as a tool whose input schema is inferred from
// In. The SDK validates arguments against that schema before the handler runs,
// so a property focal did not declare, or a value of the wrong JSON type,
// never reaches focal's own parsing.
func add[In toolArgs](srv *sdk.Server, s *server, name, summary string) {
	sdk.AddTool(srv, &sdk.Tool{
		Name:        toolName(name),
		Description: summary,
		// Every focal operation is an inspection: it reads and reports,
		// and there is no operation that could change the host.
		Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in In) (*sdk.CallToolResult, any, error) {
		return s.call(ctx, name, in)
	})
}

// resolveTarget builds the destination from the call's host and user and the
// user focal serve was started with. The layering is the same one the CLI
// applies: what the call named wins, the server's default fills in for a call
// that named nothing, and a host that carries its own user is left alone —
// a default is not an override.
//
// Naming the user twice in one call is an error rather than a silent
// preference for one of them: the two spell the same thing, and guessing which
// the caller meant is a decision focal has no basis to make. The assembled
// string goes through vo.ParseTarget like any other, so a user name that
// arrived on its own property is validated rather than trusted for it.
// resolveCallTransport parses the call's transport property, defaulting to
// ssh, and refuses identity or user alongside ssm: neither means anything to
// AWS SSM Run Command, and silently ignoring one would let an agent believe a
// property it set actually took effect.
func resolveCallTransport(dest hostArgs) (vo.Transport, *result.Error) {
	transport := vo.TransportSSH
	if dest.Transport != "" {
		var err *result.Error
		transport, err = vo.ParseTransport(dest.Transport)
		if err != nil {
			return vo.TransportSSH, err
		}
	}
	if transport != vo.TransportSSM {
		return transport, nil
	}
	for _, unsupported := range []struct {
		field string
		set   bool
	}{
		{"identity", dest.Identity != ""},
		{"user", dest.User != ""},
	} {
		if unsupported.set {
			return vo.TransportSSH, result.ValidationError(
				"field_not_supported_by_transport",
				fmt.Sprintf("%q cannot be used with transport ssm", unsupported.field),
				unsupported.field,
				[]string{"omit " + unsupported.field + ", or use transport ssh"},
			)
		}
	}
	return transport, nil
}

func resolveTarget(host, user, defaultUser string) (vo.Target, *result.Error) {
	switch {
	case user != "":
		if strings.Contains(host, "@") {
			return vo.Target{}, result.ValidationError(
				"conflicting_user",
				"the user is named twice: once in host and once in user",
				"user",
				[]string{`either "user" with a bare host, or a host written as user@host`},
			)
		}
		host = user + "@" + host
	case defaultUser != "" && !strings.Contains(host, "@"):
		host = defaultUser + "@" + host
	}
	return vo.ParseTarget(host)
}
