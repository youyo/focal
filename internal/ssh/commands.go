package ssh

import (
	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/vo"
)

// This file is the complete catalogue of remote commands Focal can run. A
// reader auditing what a coding agent is able to cause on a server needs to
// read only this file: newCommand is unexported, so a package outside
// internal/ssh can obtain a Command only by calling one of the factories here,
// and boundary_test.go fails if a factory appears that the catalogue in
// allowedCommandFactories does not list.
//
// Every factory takes the resolved policy.Policy rather than a sudo mode, so
// the privilege a command carries is always the one policy.Resolve arrived at
// by intersecting the operation's capability with the administrator's config.
//
// Three rules hold for every line below and are enforced from boundary_test.go
// rather than by review. Options and paths are lit(), so each one is a string
// literal a reader can see; anything a caller chose is val() or prefixed(),
// so it has passed internal/vo on the way in and argSpec on the way out; and a
// factory's parameters are value objects, never strings, so there is no way to
// reach an argv with an unparsed one. Nothing here builds a shell string, and
// no factory emits a pipe, a redirect or a second command — the argv is handed
// to ssh(1) as tokens and to the remote host as one program with its
// arguments.

// UptimeCommand builds the uptime(1) invocation: load averages and how long
// the host has been up.
func UptimeCommand(p policy.Policy) (Command, *result.Error) {
	return newCommand(p, "uptime")
}

// UnameCommand builds uname -a: kernel name, release and machine in one line.
func UnameCommand(p policy.Policy) (Command, *result.Error) {
	return newCommand(p, "uname", lit("-a"))
}

// HostnameCommand builds hostname(1).
func HostnameCommand(p policy.Policy) (Command, *result.Error) {
	return newCommand(p, "hostname")
}

// DateCommand builds date --iso-8601=seconds. The format is pinned so the
// answer does not depend on the remote locale, which is what makes the two
// clocks comparable when a caller is looking for drift.
func DateCommand(p policy.Policy) (Command, *result.Error) {
	return newCommand(p, "date", lit("--iso-8601=seconds"))
}

// OSReleaseCommand reads /etc/os-release, the distribution's own identity file.
func OSReleaseCommand(p policy.Policy) (Command, *result.Error) {
	return newCommand(p, "cat", lit("/etc/os-release"))
}

// LSCPUCommand builds lscpu(1): the CPU topology as the kernel reports it.
func LSCPUCommand(p policy.Policy) (Command, *result.Error) {
	return newCommand(p, "lscpu")
}

// LoadAvgCommand reads /proc/loadavg. It is separate from uptime because the
// procfs form carries the running/total process counts uptime drops.
func LoadAvgCommand(p policy.Policy) (Command, *result.Error) {
	return newCommand(p, "cat", lit("/proc/loadavg"))
}

// FreeCommand builds free -b. Bytes rather than a human-readable unit: the
// reader is an agent, and a scaled "1.5Gi" would have to be parsed back.
func FreeCommand(p policy.Policy) (Command, *result.Error) {
	return newCommand(p, "free", lit("-b"))
}

// MemInfoCommand reads /proc/meminfo for the detail free(1) summarises away.
func MemInfoCommand(p policy.Policy) (Command, *result.Error) {
	return newCommand(p, "cat", lit("/proc/meminfo"))
}

// DFCommand builds df -PT: POSIX output format, with the filesystem type
// column. -P is what keeps a long device name from wrapping onto a second
// line and turning one row into two.
func DFCommand(p policy.Policy) (Command, *result.Error) {
	return newCommand(p, "df", lit("-PT"))
}

// LSBLKCommand builds lsblk with an explicit column list, so the output does
// not change shape with the util-linux version installed on the host.
func LSBLKCommand(p policy.Policy) (Command, *result.Error) {
	return newCommand(p, "lsblk", lit("-o"), lit("NAME,TYPE,SIZE,FSTYPE,MOUNTPOINTS"))
}

// FindMntCommand builds findmnt(1): the mount tree, including the bind and
// overlay mounts df(1) does not distinguish.
func FindMntCommand(p policy.Policy) (Command, *result.Error) {
	return newCommand(p, "findmnt")
}

// IPAddrCommand builds ip -details addr show: every interface with its
// addresses, and the device details that say whether one is a bridge, a VLAN
// or a tunnel.
func IPAddrCommand(p policy.Policy) (Command, *result.Error) {
	return newCommand(p, "ip", lit("-details"), lit("addr"), lit("show"))
}

// IPRouteCommand builds ip route show for the main routing table.
func IPRouteCommand(p policy.Policy) (Command, *result.Error) {
	return newCommand(p, "ip", lit("route"), lit("show"))
}

// SocketStatsCommand builds ss -lntup: listening TCP and UDP sockets with the
// owning process, numeric so nothing waits on a DNS lookup.
func SocketStatsCommand(p policy.Policy) (Command, *result.Error) {
	return newCommand(p, "ss", lit("-lntup"))
}

// ResolvConfCommand reads /etc/resolv.conf, the resolver configuration in
// effect for processes that do not consult systemd-resolved directly.
func ResolvConfCommand(p policy.Policy) (Command, *result.Error) {
	return newCommand(p, "cat", lit("/etc/resolv.conf"))
}

// ProcessListCommand builds the one ps(1) invocation Focal makes. The column
// list is fixed and the command takes no filter: a caller asking for one
// process by name or pid gets the full list here and Focal narrows it locally,
// so no part of a filter expression is ever composed into a remote argv.
func ProcessListCommand(p policy.Policy) (Command, *result.Error) {
	return newCommand(p, "ps", lit("-eo"), lit("pid,ppid,user,state,%cpu,%mem,etime,comm,args"))
}

// ServiceStatusCommand builds systemctl show for one unit, restricted to the
// properties Focal reports. systemctl show rather than status because its
// KEY=VALUE output is a format rather than a rendering, and an explicit
// --property list because it also bounds what a remote host can put in front
// of the caller.
//
// The unit name is placed in the argv exactly as vo.ServiceName carries it.
// Appending ".service" to a bare name is internal/operation's job, and doing
// it here as well would leave the rule owned by two layers.
func ServiceStatusCommand(p policy.Policy, unit vo.ServiceName) (Command, *result.Error) {
	return newCommand(p, "systemctl",
		lit("show"),
		val(unit.String()),
		lit("--no-pager"),
		lit("--property=Id,Description,LoadState,ActiveState,SubState,MainPID,ExecMainStatus,Result"),
	)
}

// LogsCommand builds the journalctl invocation for one unit's logs.
//
// --since takes a relative span, and the "-" that makes it relative is part of
// the literal rather than of the value: vo.Duration carries "30m", the argv
// needs "--since=-30m", and joining them here means the caller's half is still
// refused if it ever begins with a hyphen of its own. -n is always passed, so
// the answer does not silently become journalctl's default of ten lines on a
// host that configured it differently.
func LogsCommand(p policy.Policy, unit vo.ServiceName, since vo.Duration, lines vo.LineLimit) (Command, *result.Error) {
	return newCommand(p, "journalctl",
		lit("-u"),
		val(unit.String()),
		prefixed("--since=-", since.String()),
		lit("-n"),
		val(lines.String()),
		lit("--no-pager"),
		lit("--output=short-iso"),
	)
}

// KernelLogsCommand builds journalctl -k: the kernel ring buffer as the
// journal holds it. It is the same shape as LogsCommand without a unit, since
// -k selects the messages instead.
func KernelLogsCommand(p policy.Policy, since vo.Duration, lines vo.LineLimit) (Command, *result.Error) {
	return newCommand(p, "journalctl",
		lit("-k"),
		prefixed("--since=-", since.String()),
		lit("-n"),
		val(lines.String()),
		lit("--no-pager"),
		lit("--output=short-iso"),
	)
}
