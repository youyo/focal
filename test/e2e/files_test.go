//go:build e2e

package e2e

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// The tests in this file run the files operation (ADR 0012) against the tree
// test/e2e/fixtures/files-fixture.sh builds in both containers, through the
// focal binary. Each runs once per container: ubuntu 24.04 (coreutils 9.4,
// UTC) and Amazon Linux 2023 (coreutils 8.32, Asia/Tokyo).

const (
	filesRoot    = "/srv/focal-e2e/files"
	filesAllowed = filesRoot + "/allowed"
)

// filesContainer is one container the files tests run against, with the
// offset its clock prints the fixture's single timestamp in.
type filesContainer struct {
	name  string
	get   func(t *testing.T) *harness
	mtime string
}

var filesContainers = []filesContainer{
	{"ubuntu", requireHarness, "2026-01-01T18:04:05+00:00"},
	{"al2023", requireAL2023, "2026-01-02T03:04:05+09:00"},
}

func forEachFilesContainer(t *testing.T, fn func(t *testing.T, h *harness, c filesContainer)) {
	t.Helper()
	for _, c := range filesContainers {
		t.Run(c.name, func(t *testing.T) {
			h := withFilesConfig(t, c.get(t))
			fn(t, h, c)
		})
	}
}

// withFilesConfig returns a harness whose config enables files for the
// fixture's allowed directory and for the symlink that points at it. It is a
// file of its own: the shared config stays on focal's defaults, which the tool
// list tests rely on.
func withFilesConfig(t *testing.T, h *harness) *harness {
	t.Helper()
	base, err := os.ReadFile(h.configFile)
	if err != nil {
		t.Fatalf("read %s: %v", h.configFile, err)
	}
	body := string(base) + "operations:\n  files:\n    enabled: true\n    paths:\n" +
		"      - " + filesAllowed + "\n" +
		"      - " + filesRoot + "/allowed-link\n"
	path := filepath.Join(t.TempDir(), "focal-files.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return &harness{focalBin: h.focalBin, configFile: path, env: h.env}
}

// filesAs lists path as user, which is e2e or e2e-env.
func filesAs(t *testing.T, h *harness, user, path string) (envelope, string) {
	t.Helper()
	stdout, stderr, code := h.run(context.Background(), nil, "--config", h.configFile, user+"@127.0.0.1", "files", path)
	if code != 0 {
		t.Fatalf("files %s as %s: exit %d, stderr: %s", path, user, code, stderr)
	}
	return decodeEnvelope(t, stdout), stdout
}

// filesFixtureEntries is what the allowed directory holds: name as ls -b
// escapes it, type, and the size of each file and symlink. Directory sizes
// depend on the filesystem and are only required to be digits.
var filesFixtureEntries = map[string][2]string{
	`a\ b\n-rw-r--r--\ 1\ 0\ 2026-01-02\ 03:04:05.000000000\ +0900\ forged`: {"file", "1"},
	`name\ with\ space\nand\ newline\ ->\ arrow`:                            {"file", "4"},
	`ends\ ->`:        {"symlink", "11"},
	"link-to-regular": {"symlink", "11"},
	"link-out":        {"symlink", "10"},
	"regular.txt":     {"file", "6"},
	"acl":             {"directory", ""},
	"appowned":        {"directory", ""},
	"grp775":          {"directory", ""},
	"noexec":          {"directory", ""},
	"nonascii":        {"directory", ""},
	"other1777":       {"directory", ""},
	"setgid":          {"directory", ""},
	"subdir":          {"directory", ""},
}

func TestFilesListsTheAllowedDirectory(t *testing.T) {
	forEachFilesContainer(t, func(t *testing.T, h *harness, c filesContainer) {
		env, raw := filesAs(t, h, "e2e", filesAllowed)
		if env.Status != "ok" || env.Error != nil {
			t.Fatalf("status = %q, error = %+v\n%s", env.Status, env.Error, raw)
		}
		want := map[string]string{"path": filesAllowed, "complete": "true", "skipped_lines": "0"}
		if !reflect.DeepEqual(env.Data, want) {
			t.Errorf("data = %v, want %v", env.Data, want)
		}
		if len(env.Items) != len(filesFixtureEntries) {
			t.Errorf("%d items, want the %d real entries: a forged line or a lost entry\n%s", len(env.Items), len(filesFixtureEntries), raw)
		}
		for _, it := range env.Items {
			if len(it) != 4 {
				t.Errorf("item %v has keys other than name, type, size, mtime", it)
			}
			exp, ok := filesFixtureEntries[it["name"]]
			if !ok {
				t.Errorf("unexpected entry %q", it["name"])
				continue
			}
			if it["type"] != exp[0] {
				t.Errorf("%q: type = %q, want %q", it["name"], it["type"], exp[0])
			}
			if exp[1] != "" && it["size"] != exp[1] {
				t.Errorf("%q: size = %q, want %q", it["name"], it["size"], exp[1])
			}
			if strings.Trim(it["size"], "0123456789") != "" || it["size"] == "" {
				t.Errorf("%q: size %q is not a byte count", it["name"], it["size"])
			}
			if it["mtime"] != c.mtime {
				t.Errorf("%q: mtime = %q, want %q", it["name"], it["mtime"], c.mtime)
			}
		}
		// A symlink's target, the outside directory's names and the raw
		// ls output never reach the caller.
		for _, leak := range []string{"../outside", "-> regular.txt", "secret-name", "total "} {
			if strings.Contains(raw, leak) {
				t.Errorf("output carries %q\n%s", leak, raw)
			}
		}
		if len(env.Parts) != 2 || env.Parts[0].Name != "check" || env.Parts[1].Name != "ls" || env.Parts[0].Stdout != "" || env.Parts[1].Stdout != "" {
			t.Errorf("parts = %+v, want check and ls without their output", env.Parts)
		}
	})
}

func TestFilesListsSubdirectoriesInTheirOwnCall(t *testing.T) {
	forEachFilesContainer(t, func(t *testing.T, h *harness, c filesContainer) {
		env, raw := filesAs(t, h, "e2e", filesAllowed+"/subdir")
		want := []map[string]string{{"name": "nested.txt", "type": "file", "size": "2", "mtime": c.mtime}}
		if env.Status != "ok" || !reflect.DeepEqual(env.Items, want) {
			t.Errorf("status = %q, items = %v, want %v\n%s", env.Status, env.Items, want, raw)
		}
		// Owned by an application, the directory itself can be listed:
		// only what lies under it is beyond root's control.
		env, raw = filesAs(t, h, "e2e", filesAllowed+"/appowned")
		if env.Status != "ok" || len(env.Items) != 1 || env.Items[0]["name"] != "archive" {
			t.Errorf("appowned: status = %q, items = %v\n%s", env.Status, env.Items, raw)
		}
		// A root directory with setgid (2755) is on a path root controls.
		env, raw = filesAs(t, h, "e2e", filesAllowed+"/setgid")
		if env.Status != "ok" || env.Error != nil || env.Data["complete"] != "true" {
			t.Errorf("setgid: status = %q, error = %+v\n%s", env.Status, env.Error, raw)
		}
	})
}

func TestFilesRefusesPathsRootDoesNotControl(t *testing.T) {
	forEachFilesContainer(t, func(t *testing.T, h *harness, _ filesContainer) {
		for _, tc := range []struct {
			name, path, prefix, reason string
		}{
			{"under an application's directory", filesAllowed + "/appowned/archive", filesAllowed + "/appowned", "not_root_owned"},
			{"under a directory a non-root group can write (775)", filesAllowed + "/grp775/inner", filesAllowed + "/grp775", "group_or_other_writable"},
			{"under a directory anyone can write (1777)", filesAllowed + "/other1777/inner", filesAllowed + "/other1777", "group_or_other_writable"},
			{"under a directory with an ACL", filesAllowed + "/acl/inner", filesAllowed + "/acl", "acl_present"},
			{"a symlink to a directory outside the list", filesAllowed + "/link-out", filesAllowed + "/link-out", "not_directory"},
			{"through a symlink in the middle of the path", filesAllowed + "/link-out/secret-dir", filesAllowed + "/link-out", "not_directory"},
			{"an allowed entry that is itself a symlink", filesRoot + "/allowed-link", filesRoot + "/allowed-link", "not_directory"},
			{"under an allowed entry that is a symlink", filesRoot + "/allowed-link/subdir", filesRoot + "/allowed-link", "not_directory"},
			{"a file", filesAllowed + "/regular.txt", filesAllowed + "/regular.txt", "not_directory"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				env, raw := filesAs(t, h, "e2e", tc.path)
				if env.Status != "failed" || env.Error == nil || env.Error.Code != "path_not_root_controlled" {
					t.Fatalf("status = %q, error = %+v, want path_not_root_controlled\n%s", env.Status, env.Error, raw)
				}
				want := map[string]string{"path": tc.path, "complete": "false", "prefix": tc.prefix, "reason": tc.reason}
				if !reflect.DeepEqual(env.Data, want) {
					t.Errorf("data = %v, want %v", env.Data, want)
				}
				if len(env.Parts) != 1 || env.Parts[0].Name != "check" {
					t.Errorf("parts = %+v, want the check alone: the listing must not run", env.Parts)
				}
				if env.Items != nil {
					t.Errorf("items = %v", env.Items)
				}
				// Neither the owner, the group, the mode, the link's
				// target nor anything outside the list is reported.
				for _, leak := range []string{"secret-name", "../outside", "focal-app", "focal-grp", "rwx", "r-x", "drwx", " -> "} {
					if strings.Contains(raw, leak) {
						t.Errorf("output carries %q\n%s", leak, raw)
					}
				}
			})
		}
	})
}

// TestFilesExitStatusOne lists a directory the SSH user may read but not
// search: ls prints its entries with every column a "?" and exits 1. Those
// lines are counted and dropped, and the answer says it is incomplete.
func TestFilesExitStatusOne(t *testing.T) {
	forEachFilesContainer(t, func(t *testing.T, h *harness, _ filesContainer) {
		env, raw := filesAs(t, h, "e2e", filesAllowed+"/noexec")
		if env.Status != "failed" || env.ExitCode != 1 || env.Error != nil {
			t.Errorf("status = %q, exit_code = %d, error = %+v, want ls's own failed and 1\n%s", env.Status, env.ExitCode, env.Error, raw)
		}
		want := map[string]string{"path": filesAllowed + "/noexec", "complete": "false", "skipped_lines": "2"}
		if !reflect.DeepEqual(env.Data, want) || len(env.Items) != 0 {
			t.Errorf("data = %v, items = %v, want %v and none", env.Data, env.Items, want)
		}
		for _, leak := range []string{"f1", "f2", "Permission denied"} {
			if strings.Contains(raw, leak) {
				t.Errorf("output carries %q\n%s", leak, raw)
			}
		}
	})
}

// TestFilesIgnoresTheSessionsBlockSizeAndLocale lists the same directories as
// e2e, e2e-env and e2e-utf8. sshd gives the last two BLOCK_SIZE=human and
// LS_BLOCK_SIZE=human, and LC_ALL=C and LC_ALL=C.UTF-8 respectively.
// --block-size=1 keeps every size the same. The non-ASCII name shows the
// environment took effect, since the two users differ in nothing else: in C ls
// escapes it and focal returns it escaped; in C.UTF-8 ls prints it raw and
// focal leaves it out and counts it.
func TestFilesIgnoresTheSessionsBlockSizeAndLocale(t *testing.T) {
	forEachFilesContainer(t, func(t *testing.T, h *harness, _ filesContainer) {
		plain, _ := filesAs(t, h, "e2e", filesAllowed)
		for _, user := range []string{"e2e-env", "e2e-utf8"} {
			withEnv, raw := filesAs(t, h, user, filesAllowed)
			if !reflect.DeepEqual(sortedItems(plain.Items), sortedItems(withEnv.Items)) {
				t.Errorf("the listing changed with %s's environment\n e2e: %v\n %s: %v", user, plain.Items, user, withEnv.Items)
			}
			if withEnv.Data["complete"] != "true" || withEnv.Data["skipped_lines"] != "0" {
				t.Errorf("%s data = %v\n%s", user, withEnv.Data, raw)
			}
		}

		c, raw := filesAs(t, h, "e2e-env", filesAllowed+"/nonascii")
		if len(c.Items) != 1 || c.Items[0]["name"] != `caf\303\251` || c.Data["complete"] != "true" {
			t.Errorf("LC_ALL=C: items = %v, data = %v, want the escaped name\n%s", c.Items, c.Data, raw)
		}
		utf8, raw := filesAs(t, h, "e2e-utf8", filesAllowed+"/nonascii")
		if len(utf8.Items) != 0 || utf8.Data["skipped_lines"] != "1" || utf8.Data["complete"] != "false" {
			t.Errorf("LC_ALL=C.UTF-8: items = %v, data = %v, want the raw name skipped\n%s", utf8.Items, utf8.Data, raw)
		}
		if strings.Contains(raw, "caf") {
			t.Errorf("the skipped name reached the output\n%s", raw)
		}

		// What the plain e2e session gets is the host's own default, which
		// focal does not fix; it is recorded, not asserted.
		def, _ := filesAs(t, h, "e2e", filesAllowed+"/nonascii")
		t.Logf("the default session: items = %v, data = %v", def.Items, def.Data)
	})
}

func sortedItems(items []map[string]string) []map[string]string {
	out := append([]map[string]string(nil), items...)
	sort.Slice(out, func(i, j int) bool { return out[i]["name"] < out[j]["name"] })
	return out
}

// TestFilesRefusesInputsBeforeConnecting names paths that never match the
// list. Each is a validation error — the class focal returns before it builds
// an executor — and the exit status is not 0.
func TestFilesRefusesInputsBeforeConnecting(t *testing.T) {
	forEachFilesContainer(t, func(t *testing.T, h *harness, _ filesContainer) {
		for _, path := range []string{
			filesAllowed + "/..",
			filesAllowed + "/../outside",
			filesAllowed + "/",
			"/srv//focal-e2e/files/allowed",
			filesAllowed + "/.",
			"srv/focal-e2e/files/allowed",
			filesRoot + "/outside",
			filesRoot + "/allowed-other",
			filesRoot,
		} {
			stdout, stderr, code := h.run(context.Background(), nil, "--config", h.configFile, targetHost, "files", path)
			if code == 0 || stdout != "" {
				t.Errorf("%q: exit %d, stdout %q, want a refusal", path, code, stdout)
				continue
			}
			e := decodeError(t, stderr)
			if e.Kind != "validation" || (e.Code != "invalid_path" && e.Code != "path_not_allowed") {
				t.Errorf("%q: error = %+v, want invalid_path or path_not_allowed", path, e)
			}
		}
	})
}

func TestFilesIsOffByDefault(t *testing.T) {
	for _, c := range filesContainers {
		t.Run(c.name, func(t *testing.T) {
			h := c.get(t)
			stdout, stderr, code := h.cli(context.Background(), "files", filesAllowed)
			if code == 0 || stdout != "" {
				t.Fatalf("exit %d, stdout %q, want a refusal", code, stdout)
			}
			if e := decodeError(t, stderr); e.Code != "operation_disabled" {
				t.Errorf("error = %+v, want operation_disabled", e)
			}
		})
	}
}
