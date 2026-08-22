package vo_test

import (
	"strings"
	"testing"

	"github.com/youyo/focal/internal/result"
	"github.com/youyo/focal/internal/vo"
)

// parser adapts one Parse function to a common shape so the shared injection
// table below can be run against every value object. A payload that must be
// rejected is rejected by all of them, not just by the one it targets.
type parser struct {
	name  string
	parse func(string) (string, *result.Error)
}

var parsers = []parser{
	{"ParseTarget", func(v string) (string, *result.Error) { t, err := vo.ParseTarget(v); return t.String(), err }},
	{"ParseServiceName", func(v string) (string, *result.Error) { s, err := vo.ParseServiceName(v); return s.String(), err }},
	{"ParseDuration", func(v string) (string, *result.Error) { d, err := vo.ParseDuration(v); return d.String(), err }},
	{"ParseLineLimit", func(v string) (string, *result.Error) { l, err := vo.ParseLineLimit(v); return l.String(), err }},
	{"ParsePID", func(v string) (string, *result.Error) { p, err := vo.ParsePID(v); return p.String(), err }},
}

// injectionInputs are the payloads no value object may ever accept. Shell
// metacharacters and command substitution cover the classic injection route;
// control characters and "%" cover the ssh_config %h/%u expansion route
// exploited by CVE-2023-51385 and CVE-2025-61984.
var injectionInputs = []struct {
	name  string
	input string
}{
	{"empty", ""},
	{"semicolon", "nginx;id"},
	{"command substitution", "$(id)"},
	{"backtick substitution", "`id`"},
	{"dollar brace", "${IFS}"},
	{"pipe", "nginx|id"},
	{"and and", "nginx&&id"},
	{"background", "nginx&"},
	{"redirect out", "nginx>/tmp/owned"},
	{"redirect in", "nginx</etc/passwd"},
	{"subshell parens", "(id)"},
	{"newline", "nginx\nid"},
	{"carriage return", "nginx\rid"},
	{"nul byte", "nginx\x00id"},
	{"vertical tab", "nginx\vid"},
	{"delete control char", "nginx\x7f"},
	{"escape control char", "nginx\x1b[0m"},
	{"space", "nginx id"},
	{"leading space", " nginx"},
	{"tab", "nginx\tid"},
	{"path traversal", "../../etc/passwd"},
	{"absolute path", "/etc/passwd"},
	{"leading hyphen option", "-oProxyCommand=evil"},
	{"leading hyphen short", "-i/tmp/key"},
	{"percent h", "%h"},
	{"percent u", "%u"},
	{"percent inside token", "nginx%h"},
	{"single quote", "nginx'id'"},
	{"double quote", "nginx\"id\""},
	{"backslash", "nginx\\id"},
	{"glob star", "nginx*"},
	{"glob question", "nginx?"},
	{"brace expansion", "{a,b}"},
	{"tilde expansion", "~root"},
	{"hash comment", "nginx#id"},
	{"exclamation", "nginx!id"},
	{"equals", "nginx=id"},
	{"plus", "nginx+id"},
	{"comma", "nginx,id"},
	{"slash", "nginx/id"},
	{"non ascii", "nginxé"},
	{"unicode fullwidth semicolon", "nginx；id"},
}

func TestParsersRejectInjectionInputs(t *testing.T) {
	for _, p := range parsers {
		for _, tc := range injectionInputs {
			t.Run(p.name+"/"+tc.name, func(t *testing.T) {
				got, err := p.parse(tc.input)
				if err == nil {
					t.Fatalf("accepted injection input %q (value %q), want rejected", tc.input, got)
				}
				assertValidationError(t, err)
				if got != "" {
					t.Errorf("rejected input produced non-zero value %q, want empty", got)
				}
			})
		}
	}
}

func TestParsersRejectOverlongInput(t *testing.T) {
	// Each parser must enforce its own byte ceiling; a single oversized
	// payload proves none of them fall back to an unbounded input.
	overlong := strings.Repeat("a", 1024)
	for _, p := range parsers {
		t.Run(p.name, func(t *testing.T) {
			if _, err := p.parse(overlong); err == nil {
				t.Fatalf("accepted %d-byte input, want rejected", len(overlong))
			}
		})
	}
}

func TestZeroValuesRenderEmpty(t *testing.T) {
	var (
		target  vo.Target
		service vo.ServiceName
		dur     vo.Duration
		lines   vo.LineLimit
		pid     vo.PID
	)
	zero := []struct {
		name string
		got  string
	}{
		{"Target", target.String()},
		{"ServiceName", service.String()},
		{"Duration", dur.String()},
		{"LineLimit", lines.String()},
		{"PID", pid.String()},
	}
	for _, z := range zero {
		if z.got != "" {
			t.Errorf("%s zero value String() = %q, want empty", z.name, z.got)
		}
	}
}

func TestParseTargetAccepts(t *testing.T) {
	accepted := []string{
		"host",
		"user@host",
		"192.0.2.10",
		"my_host.internal",
		"[2001:db8::1]",
		"user@[2001:db8::1]",
		"h",
		"web-01.example.com",
		"deploy.user@bastion.example.com",
		"u-1_2@10.0.0.1",
		"[::1]",
		"[2001:db8:0:0:0:0:0:1]",
		strings.Repeat("a", 32) + "@host",
		strings.Repeat("a", 63) + ".example.com",
	}
	for _, in := range accepted {
		t.Run(in, func(t *testing.T) {
			got, err := vo.ParseTarget(in)
			if err != nil {
				t.Fatalf("ParseTarget(%q) rejected: %v", in, err)
			}
			if got.String() != in {
				t.Errorf("String() = %q, want %q", got.String(), in)
			}
		})
	}
}

func TestParseTargetRejects(t *testing.T) {
	rejected := []struct {
		name  string
		input string
	}{
		{"port suffix", "host:22"},
		{"port suffix on ipv4", "192.0.2.10:22"},
		{"bare colon", "host:"},
		{"two at signs", "user@@host"},
		{"two at signs separated", "user@host@other"},
		{"empty user", "@host"},
		{"empty host", "user@"},
		{"empty label", "host..name"},
		{"leading dot", ".host"},
		{"trailing dot", "host."},
		{"label starts with hyphen", "-host"},
		{"label starts with underscore", "_host"},
		{"inner label starts with hyphen", "host.-evil"},
		{"user starts with hyphen", "-user@host"},
		{"user starts with dot", ".user@host"},
		{"unclosed ipv6", "[2001:db8::1"},
		{"unopened ipv6", "2001:db8::1]"},
		{"empty brackets", "[]"},
		{"bad ipv6 digits", "[gggg::1]"},
		{"ipv4 in brackets", "[1.2.3.4]"},
		{"ipv6 with zone", "[fe80::1%eth0]"},
		{"ipv6 with port", "[2001:db8::1]:22"},
		{"bare ipv6 without brackets", "2001:db8::1"},
		{"user too long", strings.Repeat("a", 33) + "@host"},
		{"label too long", strings.Repeat("a", 64) + ".example.com"},
		{"host too long", strings.Repeat("a.", 128) + "b"},
		{"user part over length", strings.Repeat("a", 316) + "@host"},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			got, err := vo.ParseTarget(tc.input)
			if err == nil {
				t.Fatalf("ParseTarget(%q) accepted (value %q), want rejected", tc.input, got.String())
			}
			assertValidationError(t, err)
		})
	}
}

func TestParseServiceNameAccepts(t *testing.T) {
	accepted := []string{
		"nginx",
		"nginx.service",
		"foo@bar.service",
		"systemd-journald.service",
		"user@1000.service",
		"dbus.socket",
		"a",
		"getty@tty1.service",
		"system-getty.slice",
		"my_service:instance",
		strings.Repeat("a", 256),
	}
	for _, in := range accepted {
		t.Run(in, func(t *testing.T) {
			got, err := vo.ParseServiceName(in)
			if err != nil {
				t.Fatalf("ParseServiceName(%q) rejected: %v", in, err)
			}
			if got.String() != in {
				t.Errorf("String() = %q, want %q", got.String(), in)
			}
		})
	}
}

func TestParseServiceNameRejects(t *testing.T) {
	rejected := []struct {
		name  string
		input string
	}{
		{"leading hyphen", "-nginx"},
		{"leading dot", ".nginx"},
		{"leading at", "@nginx"},
		{"leading colon", ":nginx"},
		{"leading underscore", "_nginx"},
		{"too long", strings.Repeat("a", 257)},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			got, err := vo.ParseServiceName(tc.input)
			if err == nil {
				t.Fatalf("ParseServiceName(%q) accepted (value %q), want rejected", tc.input, got.String())
			}
			assertValidationError(t, err)
		})
	}
}

func TestParseDurationAccepts(t *testing.T) {
	for _, in := range []string{"1s", "30s", "5m", "1h", "24h", "1d", "30d", "999999s", "43200m", "720h"} {
		t.Run(in, func(t *testing.T) {
			got, err := vo.ParseDuration(in)
			if err != nil {
				t.Fatalf("ParseDuration(%q) rejected: %v", in, err)
			}
			if got.String() != in {
				t.Errorf("String() = %q, want %q", got.String(), in)
			}
		})
	}
}

func TestParseDurationRejects(t *testing.T) {
	rejected := []struct {
		name  string
		input string
	}{
		{"no unit", "5"},
		{"unknown unit", "5w"},
		{"uppercase unit", "5S"},
		{"zero", "0s"},
		{"leading zero", "01s"},
		{"unit only", "s"},
		{"two units", "1s1s"},
		{"unit first", "s1"},
		{"fractional", "1.5h"},
		{"negative", "-1s"},
		{"over thirty days by unit", "31d"},
		{"over thirty days by hours", "721h"},
		{"over thirty days by minutes", "43201m"},
		{"seven digits", "1234567s"},
		{"go duration syntax", "1h30m"},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			got, err := vo.ParseDuration(tc.input)
			if err == nil {
				t.Fatalf("ParseDuration(%q) accepted (value %q), want rejected", tc.input, got.String())
			}
			assertValidationError(t, err)
		})
	}
}

func TestParseLineLimitAccepts(t *testing.T) {
	for _, in := range []string{"1", "10", "500", "99999", "100000"} {
		t.Run(in, func(t *testing.T) {
			got, err := vo.ParseLineLimit(in)
			if err != nil {
				t.Fatalf("ParseLineLimit(%q) rejected: %v", in, err)
			}
			if got.String() != in {
				t.Errorf("String() = %q, want %q", got.String(), in)
			}
		})
	}
}

func TestParseLineLimitRejects(t *testing.T) {
	rejected := []struct {
		name  string
		input string
	}{
		{"zero", "0"},
		{"leading zero", "0100"},
		{"above maximum", "100001"},
		{"seven digits", "1000000"},
		{"negative", "-1"},
		{"plus sign", "+1"},
		{"hex", "0x10"},
		{"underscore separator", "1_000"},
		{"fractional", "1.0"},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			got, err := vo.ParseLineLimit(tc.input)
			if err == nil {
				t.Fatalf("ParseLineLimit(%q) accepted (value %q), want rejected", tc.input, got.String())
			}
			assertValidationError(t, err)
		})
	}
}

func TestParsePIDAccepts(t *testing.T) {
	for _, in := range []string{"1", "2", "12345", "4194303", "4194304"} {
		t.Run(in, func(t *testing.T) {
			got, err := vo.ParsePID(in)
			if err != nil {
				t.Fatalf("ParsePID(%q) rejected: %v", in, err)
			}
			if got.String() != in {
				t.Errorf("String() = %q, want %q", got.String(), in)
			}
		})
	}
}

func TestParsePIDRejects(t *testing.T) {
	rejected := []struct {
		name  string
		input string
	}{
		{"zero", "0"},
		{"leading zero", "01"},
		{"above pid max limit", "4194305"},
		{"eight digits", "10000000"},
		{"negative", "-1"},
		{"plus sign", "+1"},
		{"kill all shorthand", "-1000"},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			got, err := vo.ParsePID(tc.input)
			if err == nil {
				t.Fatalf("ParsePID(%q) accepted (value %q), want rejected", tc.input, got.String())
			}
			assertValidationError(t, err)
		})
	}
}

// assertValidationError checks the contract every rejection shares: kind
// validation, a code and field naming the input, a message, and an Allowed
// list stating the accepted form.
func assertValidationError(t *testing.T, err *result.Error) {
	t.Helper()
	if err.Kind != result.KindValidation {
		t.Errorf("Kind = %q, want %q", err.Kind, result.KindValidation)
	}
	if err.Code == "" {
		t.Error("Code is empty")
	}
	if err.Message == "" {
		t.Error("Message is empty")
	}
	if err.Field == "" {
		t.Error("Field is empty")
	}
	if len(err.Allowed) == 0 {
		t.Error("Allowed is empty, want the accepted form")
	}
}
