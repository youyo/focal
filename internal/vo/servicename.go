package vo

import "github.com/youyo/focal/internal/result"

// ServiceName is a validated systemd unit name. Its zero value is not a
// usable unit name and renders as the empty string.
type ServiceName struct {
	s string
}

// String returns the unit name exactly as it was accepted.
func (s ServiceName) String() string { return s.s }

const maxServiceNameLen = 256

var serviceNameSpec = tokenSpec{
	field:   "service_name",
	subject: "service name",
	allowed: []string{
		"1-256 bytes of A-Za-z0-9:_.@- starting with a letter or digit",
		"for example nginx, nginx.service, foo@bar.service",
	},
	chars:  serviceChars,
	first:  alnumChars,
	maxLen: maxServiceNameLen,
}

// ParseServiceName accepts a systemd unit name such as nginx,
// nginx.service, or the templated form foo@bar.service.
func ParseServiceName(v string) (ServiceName, *result.Error) {
	if err := serviceNameSpec.check(v); err != nil {
		return ServiceName{}, err
	}
	return ServiceName{s: v}, nil
}
