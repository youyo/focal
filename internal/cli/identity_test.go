package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/youyo/focal/internal/result"
)

// keyFile writes a private key file with mode perm and returns its path.
func keyFile(t *testing.T, dir, name string, perm os.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("PRIVATE KEY\n"), perm); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	// WriteFile is subject to the umask, so the mode is set explicitly.
	if err := os.Chmod(path, perm); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
	return path
}

func TestNewIdentitiesAcceptsAliasEqualsPath(t *testing.T) {
	dir := t.TempDir()
	def := keyFile(t, dir, "id_ed25519", 0o600)
	customer := keyFile(t, dir, "customer-a.pem", 0o400)

	ids, err := newIdentities([]string{"default=" + def, "customer-a=" + customer})
	if err != nil {
		t.Fatalf("newIdentities: %v", err)
	}
	if got := ids.aliases(); !slices.Equal(got, []string{"customer-a", "default"}) {
		t.Errorf("aliases = %v, want them sorted", got)
	}
	for alias, want := range map[string]string{"default": def, "customer-a": customer} {
		got, resolveErr := ids.resolve(alias)
		if resolveErr != nil {
			t.Fatalf("resolve %q: %v", alias, resolveErr)
		}
		if got != want {
			t.Errorf("resolve(%q) = %q, want %q", alias, got, want)
		}
	}
}

func TestNewIdentitiesRejectsMalformedRegistrations(t *testing.T) {
	dir := t.TempDir()
	key := keyFile(t, dir, "id", 0o600)

	tests := []struct {
		name string
		spec string
		code string
	}{
		{"no separator", key, "invalid_identity_registration"},
		{"empty alias", "=" + key, "invalid_identity_alias"},
		{"empty path", "default=", "invalid_identity_registration"},
		{"alias with a space", "my key=" + key, "invalid_identity_alias"},
		{"alias with a slash", "a/b=" + key, "invalid_identity_alias"},
		{"alias too long", strings.Repeat("a", 65) + "=" + key, "invalid_identity_alias"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := newIdentities([]string{tt.spec})
			if err == nil {
				t.Fatalf("%q was accepted", tt.spec)
			}
			if err.Code != tt.code {
				t.Errorf("code = %q, want %q", err.Code, tt.code)
			}
			if err.Kind != result.KindValidation {
				t.Errorf("kind = %q, want %q", err.Kind, result.KindValidation)
			}
		})
	}
}

func TestNewIdentitiesRejectsADuplicateAlias(t *testing.T) {
	dir := t.TempDir()
	key := keyFile(t, dir, "id", 0o600)

	_, err := newIdentities([]string{"default=" + key, "default=" + key})
	if err == nil {
		t.Fatal("a repeated alias was accepted")
	}
	if err.Code != "duplicate_identity_alias" {
		t.Errorf("code = %q, want duplicate_identity_alias", err.Code)
	}
}

// TestNewIdentitiesChecksTheKeyFile is the reason aliases are registered at
// startup rather than resolved per call: a key focal serve offers has been
// through the same checks as the one in the configuration file, so there is
// no second, laxer way into an identity.
func TestNewIdentitiesChecksTheKeyFile(t *testing.T) {
	dir := t.TempDir()

	tests := []struct {
		name string
		path string
		code string
	}{
		{"missing", filepath.Join(dir, "absent"), "unreadable_identity_file"},
		{"a directory", dir, "unreadable_identity_file"},
		{"group readable", keyFile(t, dir, "group", 0o640), "insecure_identity_file_permissions"},
		{"world readable", keyFile(t, dir, "world", 0o604), "insecure_identity_file_permissions"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := newIdentities([]string{"default=" + tt.path})
			if err == nil {
				t.Fatalf("%s was accepted", tt.name)
			}
			if err.Code != tt.code {
				t.Errorf("code = %q, want %q", err.Code, tt.code)
			}
		})
	}
}

func TestNewIdentitiesExpandsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	want := keyFile(t, sshDir, "id_ed25519", 0o600)

	ids, err := newIdentities([]string{"default=~/.ssh/id_ed25519"})
	if err != nil {
		t.Fatalf("newIdentities: %v", err)
	}
	got, err := ids.resolve("default")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != want {
		t.Errorf("resolve = %q, want %q", got, want)
	}
}

// TestResolveRefusesAnAliasNobodyRegistered covers both ways an agent can name
// an identity focal does not have: a name that was never registered, and a
// path — which is only ever an unregistered name, because paths are not a
// thing this table accepts. Neither answer may name a key file: what keys
// exist on the server is not something a tool call gets to learn.
func TestResolveRefusesAnAliasNobodyRegistered(t *testing.T) {
	dir := t.TempDir()
	key := keyFile(t, dir, "id_ed25519", 0o600)
	ids, err := newIdentities([]string{"default=" + key, "customer-a=" + key})
	if err != nil {
		t.Fatalf("newIdentities: %v", err)
	}

	for _, alias := range []string{"customer-b", "~/.ssh/id_ed25519", key, "/etc/shadow"} {
		got, resolveErr := ids.resolve(alias)
		if resolveErr == nil {
			t.Errorf("resolve(%q) = %q, want a refusal", alias, got)
			continue
		}
		if resolveErr.Kind != result.KindValidation || resolveErr.Code != "unknown_identity" {
			t.Errorf("resolve(%q): error = %+v, want a validation unknown_identity", alias, resolveErr)
		}
		if strings.Contains(resolveErr.Message, key) || strings.Contains(resolveErr.Message, dir) {
			t.Errorf("resolve(%q) leaked a key path: %q", alias, resolveErr.Message)
		}
		if !slices.Equal(resolveErr.Allowed, []string{"customer-a", "default"}) {
			t.Errorf("allowed = %v, want the registered aliases", resolveErr.Allowed)
		}
	}
}

func TestResolveWithoutAnAliasUsesTheDefaultRegistration(t *testing.T) {
	dir := t.TempDir()
	key := keyFile(t, dir, "id_ed25519", 0o600)

	withDefault, err := newIdentities([]string{"default=" + key})
	if err != nil {
		t.Fatalf("newIdentities: %v", err)
	}
	got, err := withDefault.resolve("")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != key {
		t.Errorf("resolve(\"\") = %q, want the default registration %q", got, key)
	}

	// With no default registered, naming no identity leaves the choice to
	// whatever the configuration file and ssh(1) decide.
	none, err := newIdentities([]string{"customer-a=" + key})
	if err != nil {
		t.Fatalf("newIdentities: %v", err)
	}
	if got, err := none.resolve(""); err != nil || got != "" {
		t.Errorf("resolve(\"\") = %q, %v; want the empty string and no error", got, err)
	}
}
