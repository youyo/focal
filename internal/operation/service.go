package operation

import (
	"context"
	"strings"

	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/ssh"
	"github.com/youyo/focal/internal/vo"
)

// Service is the service operation: the state of one systemd unit, reported as
// KEY=VALUE properties rather than as systemctl status' human rendering.
//
// It holds the unit already normalized, so the ".service" a bare name needs is
// decided once — at construction, by normalizeUnitName, which is the same
// helper LogsCommand's caller uses — and never again while the operation runs.
// Nothing in this file appends a suffix of its own; a second place that knew
// the rule would be a second place it could change.
type Service struct {
	unit vo.ServiceName
}

var _ Operation = Service{}

// NewService builds the operation for the named unit. A bare name such as
// "nginx" becomes "nginx.service"; a name that already carries a unit type,
// such as "docker.socket", is left as it is. The normalized form is validated
// again before it is stored, so the value the argv receives is one
// vo.ParseServiceName accepted rather than one assembled after the check.
func NewService(name string) (Service, *result.Error) {
	parsed, err := vo.ParseServiceName(name)
	if err != nil {
		return Service{}, err
	}
	unit, err := vo.ParseServiceName(normalizeUnitName(parsed.String()))
	if err != nil {
		return Service{}, err
	}
	return Service{unit: unit}, nil
}

// Name returns the operation name.
func (o Service) Name() string { return "service" }

// Execute runs systemctl show for the unit and reports its properties both as
// raw output and as parsed key/value pairs.
func (o Service) Execute(ctx context.Context, ex ssh.Executor, t vo.Target, p policy.Policy) (result.Envelope, error) {
	cmd, cmdErr := ssh.ServiceStatusCommand(p, o.unit)
	if cmdErr != nil {
		return result.Envelope{}, cmdErr
	}

	out, execErr := ex.Execute(ctx, t, cmd)
	if execErr != nil {
		return result.Envelope{}, execErr
	}

	part := buildPart(o.Name(), out, nil)
	status, exitCode, durationMs, truncated := aggregate([]result.Part{part})
	return result.Envelope{
		Operation:  o.Name(),
		Host:       t.String(),
		Status:     status,
		ExitCode:   exitCode,
		DurationMs: durationMs,
		Truncated:  truncated,
		Stdout:     part.Stdout,
		Stderr:     part.Stderr,
		Data:       serviceParse(part.Stdout),
		Error:      part.Error,
	}, nil
}

// serviceProperties is the set of keys Data may contain: exactly the properties
// ssh.ServiceStatusCommand asks systemctl show for, and nothing else.
//
// The allowlist is what keeps the shape of Data a decision Focal made rather
// than one the remote host makes. systemctl show will happily print properties
// nobody asked for if a future --property list is dropped or a host answers
// more than it was asked, and Data is the field an agent reads as structured
// truth; admitting an unrequested key there would let a compromised or merely
// chatty host put arbitrary names and values into it. Stdout still carries
// everything that arrived, so nothing is hidden — it is just not promoted.
// service_test.go fails if this set and the argv's --property list diverge.
var serviceProperties = map[string]bool{
	"Id":             true,
	"Description":    true,
	"LoadState":      true,
	"ActiveState":    true,
	"SubState":       true,
	"MainPID":        true,
	"ExecMainStatus": true,
	"Result":         true,
}

// serviceParse turns systemctl show output into the requested properties. It
// returns nil rather than an empty map when nothing was recognized, so an
// answer that carried no properties at all — a unit that does not exist, an
// error message on stdout — leaves Data absent from the JSON instead of present
// and empty.
//
// A value may itself contain "=", so only the first one separates the key.
func serviceParse(stdout string) map[string]string {
	var data map[string]string
	for line := range strings.SplitSeq(stdout, "\n") {
		key, value, found := strings.Cut(line, "=")
		if !found || !serviceProperties[key] {
			continue
		}
		if data == nil {
			data = make(map[string]string, len(serviceProperties))
		}
		data[key] = value
	}
	return data
}
