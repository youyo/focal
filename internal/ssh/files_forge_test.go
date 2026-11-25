package ssh

import (
	"testing"
	"unsafe"

	"github.com/youyo/focal/internal/policy"
	"github.com/youyo/focal/internal/vo"
)

// forgeFilesPath builds a vo.FilesPath holding s without going through
// vo.ParseFilesPath, the way code that imported unsafe could. Non-test code
// may not import unsafe (internal/files_guard_test.go), so this is the one
// place such a value exists: to show what FilesCommands does with it.
func forgeFilesPath(s string) vo.FilesPath {
	return *(*vo.FilesPath)(unsafe.Pointer(&s)) //nolint:gosec // forging the value is the point of the test
}

// TestFilesCommandsRecheckTheShapeOfTheirPath pins the second check in the
// factory: a FilesPath that did not come from the parser still cannot name a
// relative path or step out with "..".
func TestFilesCommandsRecheckTheShapeOfTheirPath(t *testing.T) {
	if got := forgeFilesPath("/var/log").String(); got != "/var/log" {
		t.Fatalf("the forgery helper does not produce the value it claims: %q", got)
	}
	for _, s := range []string{
		"var/log",
		"/var/log/",
		"/var//log",
		"/var/log/../../etc",
		"/var/./log",
		"/",
	} {
		t.Run(s, func(t *testing.T) {
			check, list, err := FilesCommands(policy.Policy{}, forgeFilesPath(s))
			if err == nil {
				t.Fatalf("FilesCommands built %v and %v from a forged %q", check.Args(), list.Args(), s)
			}
			if !check.IsZero() || !list.IsZero() {
				t.Error("a refused FilesCommands handed back a Command")
			}
		})
	}
}
