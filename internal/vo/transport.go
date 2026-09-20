package vo

import (
	"strings"

	"github.com/youyo/focal/internal/result"
)

// Transport says which mechanism carries a Command to a host: ssh(1) or AWS
// SSM Run Command. It is a value object like every other type in this
// package, so a caller-supplied string reaches the rest of Focal only after
// ParseTransport has accepted it.
//
// The zero value is TransportSSH, so a Transport that was never explicitly
// parsed (a zero-value struct field in an older test, for instance) behaves
// as focal always has rather than as a transport nobody chose.
type Transport uint8

const (
	// TransportSSH runs a Command through the system's ssh(1) client.
	TransportSSH Transport = iota
	// TransportSSM runs a Command through AWS Systems Manager Run Command.
	TransportSSM
)

// transports is the declarative mapping between Transport and its wire form
// on the command line, in a tool call, and in error messages. String,
// ParseTransport and the error reporting all read this one table.
var transports = []struct {
	transport Transport
	name      string
}{
	{TransportSSH, "ssh"},
	{TransportSSM, "ssm"},
}

// String returns the wire form ("ssh", "ssm").
func (t Transport) String() string {
	for _, tr := range transports {
		if tr.transport == t {
			return tr.name
		}
	}
	return "invalid"
}

// TransportNames returns the accepted wire forms, for error reporting and for
// enumerating the config file's execution.transports values.
func TransportNames() []string {
	names := make([]string, 0, len(transports))
	for _, tr := range transports {
		names = append(names, tr.name)
	}
	return names
}

// ParseTransport converts the wire form ("ssh" or "ssm") to a Transport.
// Matching is exact: no case folding and no surrounding whitespace, so a typo
// on the command line, in a tool call, or in the config file is reported
// rather than guessed at.
func ParseTransport(s string) (Transport, *result.Error) {
	for _, tr := range transports {
		if tr.name == s {
			return tr.transport, nil
		}
	}
	return TransportSSH, result.ValidationError(
		"invalid_transport",
		"transport must be one of "+strings.Join(TransportNames(), ", "),
		"transport",
		TransportNames(),
	)
}
