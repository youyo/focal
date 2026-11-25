package vo_test

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/youyo/focal/internal/vo"
)

func mustFilesAllowList(t *testing.T, entries ...string) vo.FilesAllowList {
	t.Helper()
	list, err := vo.ParseFilesAllowList(entries)
	if err != nil {
		t.Fatalf("ParseFilesAllowList(%q): %v", entries, err)
	}
	return list
}

func TestParseFilesAllowListAccepts(t *testing.T) {
	for _, entries := range [][]string{
		nil,
		{},
		{"/var/log"},
		{"/var/log", "/etc/nginx"},
		// An entry inside another is allowed: they overlap, they are not
		// the same entry.
		{"/var/log", "/var/log/nginx"},
		{"/srv/app_1/releases-2026.11", "/opt/a+b=c:d,e@f"},
		{"/" + strings.Repeat("a", 511)},
		// Names that merely start like a refused tree are not inside it.
		{"/devices", "/process", "/system"},
	} {
		t.Run(strings.Join(entries, ","), func(t *testing.T) {
			list, err := vo.ParseFilesAllowList(entries)
			if err != nil {
				t.Fatalf("ParseFilesAllowList(%q) rejected: %v", entries, err)
			}
			if got := list.Entries(); !slices.Equal(got, entries) {
				t.Errorf("Entries() = %q, want %q", got, entries)
			}
		})
	}
}

func TestParseFilesAllowListRejects(t *testing.T) {
	tooMany := make([]string, 33)
	for i := range tooMany {
		tooMany[i] = "/srv/d" + strconv.Itoa(i)
	}
	for _, tc := range []struct {
		name    string
		entries []string
		code    string
	}{
		{"root alone", []string{"/"}, "invalid_files_path_entry"},
		{"relative", []string{"var/log"}, "invalid_files_path_entry"},
		{"dot relative", []string{"./var/log"}, "invalid_files_path_entry"},
		{"empty", []string{""}, "invalid_files_path_entry"},
		{"trailing slash", []string{"/var/log/"}, "invalid_files_path_entry"},
		{"double slash", []string{"/var//log"}, "invalid_files_path_entry"},
		{"leading double slash", []string{"//var/log"}, "invalid_files_path_entry"},
		{"dot element", []string{"/var/./log"}, "invalid_files_path_entry"},
		{"dot dot element", []string{"/var/log/../lib"}, "invalid_files_path_entry"},
		{"trailing dot", []string{"/var/log/."}, "invalid_files_path_entry"},
		{"space", []string{"/var/my log"}, "invalid_files_path_entry"},
		{"tab", []string{"/var/log\tx"}, "invalid_files_path_entry"},
		{"newline", []string{"/var/log\nx"}, "invalid_files_path_entry"},
		{"shell metacharacter", []string{"/var/log;id"}, "invalid_files_path_entry"},
		{"glob", []string{"/var/log/*"}, "invalid_files_path_entry"},
		{"percent", []string{"/var/%h"}, "invalid_files_path_entry"},
		{"non ascii", []string{"/var/logé"}, "invalid_files_path_entry"},
		{"too long", []string{"/" + strings.Repeat("a", 512)}, "invalid_files_path_entry"},
		{"proc", []string{"/proc"}, "forbidden_files_path_entry"},
		{"under proc", []string{"/proc/self"}, "forbidden_files_path_entry"},
		{"sys", []string{"/sys"}, "forbidden_files_path_entry"},
		{"under sys", []string{"/sys/fs/cgroup"}, "forbidden_files_path_entry"},
		{"dev", []string{"/dev"}, "forbidden_files_path_entry"},
		{"under dev", []string{"/dev/shm"}, "forbidden_files_path_entry"},
		{"duplicate", []string{"/var/log", "/etc", "/var/log"}, "duplicate_files_path_entry"},
		{"thirty three entries", tooMany, "too_many_files_path_entries"},
		{"second entry is bad", []string{"/var/log", "relative"}, "invalid_files_path_entry"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			list, err := vo.ParseFilesAllowList(tc.entries)
			if err == nil {
				t.Fatalf("ParseFilesAllowList(%q) accepted (%q), want rejected", tc.entries, list.Entries())
			}
			assertValidationError(t, err)
			if err.Code != tc.code {
				t.Errorf("Code = %q, want %q", err.Code, tc.code)
			}
			if len(list.Entries()) != 0 {
				t.Errorf("rejected list carries entries %q", list.Entries())
			}
			// The administrator's value is named by its position, not
			// repeated back.
			for _, e := range tc.entries {
				if len(e) > 1 && strings.Contains(err.Message, e) {
					t.Errorf("message %q repeats the entry %q", err.Message, e)
				}
			}
		})
	}
}

func TestParseFilesAllowListAtTheCeiling(t *testing.T) {
	entries := make([]string, 32)
	for i := range entries {
		entries[i] = "/srv/d" + strconv.Itoa(i)
	}
	if _, err := vo.ParseFilesAllowList(entries); err != nil {
		t.Fatalf("32 entries rejected: %v", err)
	}
}

// TestFilesAllowListIsACopy pins that neither the slice handed in nor the one
// handed out is the list a path is matched against.
func TestFilesAllowListIsACopy(t *testing.T) {
	in := []string{"/var/log"}
	list := mustFilesAllowList(t, in...)
	in[0] = "/etc"
	if _, err := vo.ParseFilesPath("/etc", list); err == nil {
		t.Error("rewriting the slice passed to ParseFilesAllowList widened the list")
	}
	out := list.Entries()
	out[0] = "/etc"
	if _, err := vo.ParseFilesPath("/etc", list); err == nil {
		t.Error("rewriting the slice Entries returned widened the list")
	}
	if _, err := vo.ParseFilesPath("/var/log", list); err != nil {
		t.Errorf("the original entry stopped matching: %v", err)
	}
}

func TestParseFilesPathAccepts(t *testing.T) {
	list := mustFilesAllowList(t, "/var/log", "/etc/nginx")
	for _, in := range []string{
		"/var/log",
		"/var/log/nginx",
		"/var/log/nginx/old",
		"/etc/nginx",
		"/etc/nginx/conf.d",
		"/var/log/a_b-c.d+e=f:g,h@i",
	} {
		t.Run(in, func(t *testing.T) {
			p, err := vo.ParseFilesPath(in, list)
			if err != nil {
				t.Fatalf("ParseFilesPath(%q) rejected: %v", in, err)
			}
			if p.String() != in {
				t.Errorf("String() = %q, want %q", p.String(), in)
			}
			if p.IsZero() {
				t.Error("accepted path reports IsZero")
			}
		})
	}
}

func TestParseFilesPathRejects(t *testing.T) {
	list := mustFilesAllowList(t, "/var/log", "/etc/nginx")
	for _, tc := range []struct {
		name string
		in   string
		code string
	}{
		{"empty", "", "invalid_path"},
		{"relative", "var/log", "invalid_path"},
		{"dot relative", "./var/log", "invalid_path"},
		{"trailing slash", "/var/log/", "invalid_path"},
		{"double slash", "/var//log", "invalid_path"},
		{"dot element", "/var/log/./nginx", "invalid_path"},
		{"dot dot escaping", "/var/log/../../etc/shadow", "invalid_path"},
		{"dot dot inside", "/var/log/nginx/..", "invalid_path"},
		{"space", "/var/log/my app", "invalid_path"},
		{"control character", "/var/log/\x1b", "invalid_path"},
		{"newline", "/var/log\n/etc", "invalid_path"},
		{"non ascii", "/var/log/é", "invalid_path"},
		{"too long", "/var/log/" + strings.Repeat("a", 504), "invalid_path"},
		{"root", "/", "path_not_allowed"},
		{"sibling sharing a prefix", "/var/log2", "path_not_allowed"},
		{"sibling sharing a longer prefix", "/var/logrotate.d", "path_not_allowed"},
		{"parent of an entry", "/var", "path_not_allowed"},
		{"sibling of an entry", "/var/lib", "path_not_allowed"},
		{"elsewhere", "/home/deploy", "path_not_allowed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := vo.ParseFilesPath(tc.in, list)
			if err == nil {
				t.Fatalf("ParseFilesPath(%q) accepted (%q), want rejected", tc.in, p.String())
			}
			assertValidationError(t, err)
			if err.Code != tc.code {
				t.Errorf("Code = %q, want %q", err.Code, tc.code)
			}
			if !p.IsZero() {
				t.Errorf("rejected input produced %q, want the zero value", p.String())
			}
		})
	}
}

func TestParseFilesPathAt512Bytes(t *testing.T) {
	in := "/var/log/" + strings.Repeat("a", 503)
	if len(in) != 512 {
		t.Fatalf("fixture is %d bytes", len(in))
	}
	if _, err := vo.ParseFilesPath(in, mustFilesAllowList(t, "/var/log")); err != nil {
		t.Fatalf("512-byte path rejected: %v", err)
	}
}

func TestParseFilesPathNotAllowedNamesTheEntries(t *testing.T) {
	list := mustFilesAllowList(t, "/var/log", "/etc/nginx")
	_, err := vo.ParseFilesPath("/home", list)
	if err == nil {
		t.Fatal("accepted a path outside the list")
	}
	if !slices.Equal(err.Allowed, []string{"/var/log", "/etc/nginx"}) {
		t.Errorf("Allowed = %q, want the configured entries", err.Allowed)
	}
	if err.Field != "path" {
		t.Errorf("Field = %q, want path", err.Field)
	}
}

func TestParseFilesPathWithAnEmptyListAllowsNothing(t *testing.T) {
	for _, list := range []vo.FilesAllowList{{}, mustFilesAllowList(t)} {
		if p, err := vo.ParseFilesPath("/var/log", list); err == nil {
			t.Errorf("an empty list allowed %q", p.String())
		} else if err.Code != "path_not_allowed" {
			t.Errorf("Code = %q, want path_not_allowed", err.Code)
		}
	}
}

func TestFilesPathZeroValue(t *testing.T) {
	var p vo.FilesPath
	if !p.IsZero() {
		t.Error("zero FilesPath does not report IsZero")
	}
	if p.String() != "" {
		t.Errorf("zero FilesPath renders %q", p.String())
	}
	if c := p.Chain(); len(c) != 0 {
		t.Errorf("zero FilesPath has a chain %q", c)
	}
}

func TestFilesPathChain(t *testing.T) {
	list := mustFilesAllowList(t, "/var", "/a")
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"/var", []string{"/", "/var"}},
		{"/var/log", []string{"/", "/var", "/var/log"}},
		{"/var/log/nginx", []string{"/", "/var", "/var/log", "/var/log/nginx"}},
		{"/a/b/c/d", []string{"/", "/a", "/a/b", "/a/b/c", "/a/b/c/d"}},
	} {
		t.Run(tc.in, func(t *testing.T) {
			p, err := vo.ParseFilesPath(tc.in, list)
			if err != nil {
				t.Fatalf("ParseFilesPath(%q): %v", tc.in, err)
			}
			got := p.Chain()
			if !slices.Equal(got, tc.want) {
				t.Errorf("Chain() = %q, want %q", got, tc.want)
			}
			// The chain is a copy: changing it changes nothing the path
			// will hand out next.
			got[len(got)-1] = "/etc"
			if again := p.Chain(); !slices.Equal(again, tc.want) {
				t.Errorf("Chain() after rewriting a previous result = %q", again)
			}
		})
	}
}

func TestParseFilesPathRejectsInjectionInputs(t *testing.T) {
	list := mustFilesAllowList(t, "/var/log")
	for _, tc := range injectionInputs {
		t.Run(tc.name, func(t *testing.T) {
			for _, in := range []string{tc.input, "/var/log/" + tc.input} {
				got, err := vo.ParseFilesPath(in, list)
				if err == nil && strings.ContainsAny(in, " \t\r\n\x00\v\x1b\x7f;$`|&<>()'\"\\*?{}~#!%") {
					t.Fatalf("accepted %q (value %q)", in, got.String())
				}
				if err != nil {
					assertValidationError(t, err)
					if !got.IsZero() {
						t.Errorf("rejected input produced %q", got.String())
					}
				}
			}
		})
	}
}
