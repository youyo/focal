package vo_test

import (
	"strings"
	"testing"

	"github.com/youyo/focal/internal/vo"
)

// The value objects in this file carry the parts of a logs request that go
// beyond a relative span and a line count: an absolute instant, the text a
// caller filters entries by, and the bucket a count is taken over.

func TestParseTimestampAccepts(t *testing.T) {
	for _, tc := range []struct {
		in   string
		unix int64
	}{
		{"2026-11-20T09:00:00+09:00", 1795132800},
		{"2026-11-20T00:00:00Z", 1795132800},
		{"2026-11-19T19:00:00-05:00", 1795132800},
		{"2026-11-20T09:00:01+09:00", 1795132801},
		{"1970-01-01T00:00:01Z", 1},
	} {
		t.Run(tc.in, func(t *testing.T) {
			got, err := vo.ParseTimestamp(tc.in)
			if err != nil {
				t.Fatalf("ParseTimestamp(%q) rejected: %v", tc.in, err)
			}
			if got.String() != tc.in {
				t.Errorf("String() = %q, want %q", got.String(), tc.in)
			}
			if got.Unix() != tc.unix {
				t.Errorf("Unix() = %d, want %d", got.Unix(), tc.unix)
			}
		})
	}
}

func TestParseTimestampRejects(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"no offset", "2026-11-20T09:00:00"},
		{"fractional seconds", "2026-11-20T09:00:00.5+09:00"},
		{"fractional seconds in utc", "2026-11-20T09:00:00.000Z"},
		{"offset without colon", "2026-11-20T09:00:00+0900"},
		{"space instead of T", "2026-11-20 09:00:00+09:00"},
		{"lowercase t", "2026-11-20t09:00:00+09:00"},
		{"lowercase z", "2026-11-20T09:00:00z"},
		{"date only", "2026-11-20"},
		{"no seconds", "2026-11-20T09:00+09:00"},
		{"month out of range", "2026-13-20T09:00:00+09:00"},
		{"day out of range", "2026-02-30T09:00:00+09:00"},
		{"hour out of range", "2026-11-20T24:00:00+09:00"},
		{"leap second", "2026-11-20T08:59:60+09:00"},
		{"the epoch itself", "1970-01-01T00:00:00Z"},
		{"before the epoch", "1969-12-31T23:59:59Z"},
		{"relative span", "30m"},
		{"epoch form", "@1795651200"},
		{"trailing text", "2026-11-20T09:00:00+09:00;id"},
		{"leading hyphen", "-2026-11-20T09:00:00+09:00"},
		{"overlong", strings.Repeat("2", 64)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := vo.ParseTimestamp(tc.input)
			if err == nil {
				t.Fatalf("ParseTimestamp(%q) = %q, want rejected", tc.input, got.String())
			}
			assertValidationError(t, err)
			if err.Code != "invalid_timestamp" {
				t.Errorf("Code = %q, want invalid_timestamp", err.Code)
			}
			if got.Unix() != 0 || got.Epoch() != "" {
				t.Errorf("rejected input produced a non-zero Timestamp: %d %q", got.Unix(), got.Epoch())
			}
		})
	}
}

func TestTimestampEpochIsPositiveDecimalDigits(t *testing.T) {
	ts, err := vo.ParseTimestamp("2026-11-20T09:00:00+09:00")
	if err != nil {
		t.Fatalf("ParseTimestamp() = %v", err)
	}
	if got := ts.Epoch(); got != "1795132800" {
		t.Errorf("Epoch() = %q, want 1795132800", got)
	}
	var zero vo.Timestamp
	if got := zero.Epoch(); got != "" {
		t.Errorf("zero Timestamp Epoch() = %q, want empty so that no argv can carry it", got)
	}
}

func TestTimestampFromUnix(t *testing.T) {
	ts, err := vo.TimestampFromUnix(1795132800)
	if err != nil {
		t.Fatalf("TimestampFromUnix() = %v", err)
	}
	if ts.Unix() != 1795132800 || ts.Epoch() != "1795132800" {
		t.Errorf("TimestampFromUnix(1795132800) = %d %q", ts.Unix(), ts.Epoch())
	}
	if got := ts.String(); got != "2026-11-20T00:00:00Z" {
		t.Errorf("String() = %q, want the instant in UTC", got)
	}
	for _, secs := range []int64{0, -1} {
		if got, err := vo.TimestampFromUnix(secs); err == nil {
			t.Errorf("TimestampFromUnix(%d) = %q, want rejected", secs, got.Epoch())
		} else {
			assertValidationError(t, err)
		}
	}
}

func TestDurationOfSecondsPicksTheLargestExactUnit(t *testing.T) {
	for _, tc := range []struct {
		secs int64
		want string
	}{
		{1, "1s"},
		{90, "90s"},
		{1800, "30m"},
		{9000, "150m"},
		{7200, "2h"},
		{86400, "1d"},
		{12*86400 + 1800, "17310m"},
		{30 * 86400, "30d"},
		{999999, "999999s"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			got, err := vo.DurationOfSeconds(tc.secs)
			if err != nil {
				t.Fatalf("DurationOfSeconds(%d) = %v", tc.secs, err)
			}
			if got.String() != tc.want || got.Seconds() != tc.secs {
				t.Errorf("DurationOfSeconds(%d) = %q (%ds), want %q", tc.secs, got.String(), got.Seconds(), tc.want)
			}
			// What it renders is a Duration ParseDuration would accept
			// back, so the argv form stays the one that type documents.
			back, err := vo.ParseDuration(got.String())
			if err != nil || back.Seconds() != tc.secs {
				t.Errorf("ParseDuration(%q) = %v, %v; want %d seconds", got.String(), back.Seconds(), err, tc.secs)
			}
		})
	}
}

func TestDurationOfSecondsRejects(t *testing.T) {
	for _, tc := range []struct {
		name string
		secs int64
	}{
		{"zero", 0},
		{"negative", -60},
		{"above 30d", 30*86400 + 60},
		{"no unit renders it in six digits", 1000001},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := vo.DurationOfSeconds(tc.secs)
			if err == nil {
				t.Fatalf("DurationOfSeconds(%d) = %q, want rejected", tc.secs, got.String())
			}
			assertValidationError(t, err)
			if err.Code != "invalid_duration" {
				t.Errorf("Code = %q, want invalid_duration", err.Code)
			}
		})
	}
}

func TestParseLogPatternAccepts(t *testing.T) {
	for _, in := range []string{
		"a",
		"connection refused",
		"ERROR; $(id) | `x` > /tmp/y",
		"-starts with a hyphen",
		"%h and ~ and #",
		"日本語のメッセージ",
		strings.Repeat("x", 256),
	} {
		t.Run(in, func(t *testing.T) {
			got, err := vo.ParseLogPattern(in)
			if err != nil {
				t.Fatalf("ParseLogPattern(%q) rejected: %v", in, err)
			}
			if got.String() != in {
				t.Errorf("String() = %q, want %q", got.String(), in)
			}
		})
	}
}

func TestParseLogPatternRejects(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"over 256 bytes", strings.Repeat("x", 257)},
		{"multibyte over 256 bytes", strings.Repeat("あ", 86)},
		{"nul", "a\x00b"},
		{"newline", "a\nb"},
		{"carriage return", "a\rb"},
		{"tab", "a\tb"},
		{"escape", "a\x1b[0m"},
		{"unit separator", "a\x1fb"},
		{"delete", "a\x7f"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := vo.ParseLogPattern(tc.input)
			if err == nil {
				t.Fatalf("ParseLogPattern(%q) = %q, want rejected", tc.input, got.String())
			}
			assertValidationError(t, err)
			if err.Code != "invalid_log_pattern" {
				t.Errorf("Code = %q, want invalid_log_pattern", err.Code)
			}
			if strings.Contains(err.Message, tc.input) && tc.input != "" {
				t.Errorf("Message %q repeats the rejected pattern", err.Message)
			}
		})
	}
}

func TestLogPatternMatchesASubstringCaseSensitively(t *testing.T) {
	p, err := vo.ParseLogPattern("Refused")
	if err != nil {
		t.Fatalf("ParseLogPattern() = %v", err)
	}
	if !p.Matches("connection Refused by peer") {
		t.Error("Matches() = false for a text that contains the pattern")
	}
	if p.Matches("connection refused by peer") {
		t.Error("Matches() = true for a text that differs only in case")
	}
	if p.Matches("R.fused") {
		t.Error("Matches() treated the pattern as anything but fixed text")
	}
	if dot, _ := vo.ParseLogPattern("a.c"); dot.Matches("abc") {
		t.Error(`Matches("abc") = true for the fixed text "a.c"`)
	}
}

func TestParseLogRegex(t *testing.T) {
	re, err := vo.ParseLogRegex(`marker line [13] of 3`)
	if err != nil {
		t.Fatalf("ParseLogRegex() = %v", err)
	}
	if re.String() != `marker line [13] of 3` {
		t.Errorf("String() = %q", re.String())
	}
	for in, want := range map[string]bool{
		"x: marker line 1 of 3": true,
		"x: marker line 2 of 3": false,
		"x: marker line 3 of 3": true,
		"x: MARKER LINE 1 OF 3": false,
	} {
		if got := re.Matches(in); got != want {
			t.Errorf("Matches(%q) = %t, want %t", in, got, want)
		}
	}
}

func TestParseLogRegexRejects(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
		code  string
	}{
		{"empty", "", "invalid_log_regex"},
		{"control character", "a\nb", "invalid_log_regex"},
		{"over 256 bytes", strings.Repeat("a", 257), "invalid_log_regex"},
		{"unbalanced paren", "secret(", "invalid_log_regex"},
		{"backreference is not RE2", `(a)\1`, "invalid_log_regex"},
		{"lookahead is not RE2", `a(?=b)`, "invalid_log_regex"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := vo.ParseLogRegex(tc.input)
			if err == nil {
				t.Fatalf("ParseLogRegex(%q) = %q, want rejected", tc.input, got.String())
			}
			assertValidationError(t, err)
			if err.Code != tc.code {
				t.Errorf("Code = %q, want %q", err.Code, tc.code)
			}
			if tc.input != "" && strings.Contains(err.Message, tc.input) {
				t.Errorf("Message %q repeats the rejected expression", err.Message)
			}
		})
	}
	var zero vo.LogRegex
	if zero.Matches("anything") {
		t.Error("the zero LogRegex matches text")
	}
}

func TestParseCountBy(t *testing.T) {
	for _, in := range []string{"hour", "day"} {
		got, err := vo.ParseCountBy(in)
		if err != nil {
			t.Fatalf("ParseCountBy(%q) = %v", in, err)
		}
		if got.String() != in {
			t.Errorf("String() = %q, want %q", got.String(), in)
		}
	}
	for _, in := range []string{"", "Hour", "minute", "week", "hours", "1h", "hour;id"} {
		got, err := vo.ParseCountBy(in)
		if err == nil {
			t.Fatalf("ParseCountBy(%q) = %q, want rejected", in, got.String())
		}
		assertValidationError(t, err)
		if err.Code != "invalid_count_by" {
			t.Errorf("Code = %q, want invalid_count_by", err.Code)
		}
	}
}
