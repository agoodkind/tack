package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// TestGitConfigHomeFileWinsOverXDG pins git's own precedence. Git reads both
// the XDG file and the home file and takes the home file's value, so an
// operator with an identity in each would otherwise be attributed to the wrong
// person.
func TestGitConfigHomeFileWinsOverXDG(t *testing.T) {
	home := t.TempDir()
	xdg := t.TempDir()
	xdgDir := filepath.Join(xdg, "git")
	if err := os.MkdirAll(xdgDir, 0o750); err != nil {
		t.Fatalf("create xdg dir: %v", err)
	}
	writeIdentityFile(t, filepath.Join(xdgDir, "config"),
		"[user]\n\tname = XDG Person\n\temail = xdg@goodkind.io\n")
	writeIdentityFile(t, filepath.Join(home, ".gitconfig"),
		"[user]\n\tname = Home Person\n\temail = home@goodkind.io\n")

	t.Setenv("GIT_CONFIG_GLOBAL", "")
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("HOME", home)

	principal, err := GitConfigOperatorSource{}.Resolve(t.Context())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if principal.Email != "home@goodkind.io" {
		t.Fatalf("email = %q, want the home file to win", principal.Email)
	}
	if principal.Name != "Home Person" {
		t.Fatalf("name = %q, want the home file to win", principal.Name)
	}
}

// TestGitConfigXDGSuppliesWhatHomeOmits pins that the two files merge rather
// than one replacing the other wholesale, which is also what git does.
func TestGitConfigXDGSuppliesWhatHomeOmits(t *testing.T) {
	home := t.TempDir()
	xdg := t.TempDir()
	xdgDir := filepath.Join(xdg, "git")
	if err := os.MkdirAll(xdgDir, 0o750); err != nil {
		t.Fatalf("create xdg dir: %v", err)
	}
	writeIdentityFile(t, filepath.Join(xdgDir, "config"),
		"[user]\n\tname = Only In XDG\n\temail = shared@goodkind.io\n")
	writeIdentityFile(t, filepath.Join(home, ".gitconfig"),
		"[user]\n\temail = home@goodkind.io\n")

	t.Setenv("GIT_CONFIG_GLOBAL", "")
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("HOME", home)

	principal, err := GitConfigOperatorSource{}.Resolve(t.Context())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if principal.Email != "home@goodkind.io" {
		t.Fatalf("email = %q, want the home file to win", principal.Email)
	}
	if principal.Name != "Only In XDG" {
		t.Fatalf("name = %q, want the XDG name to survive", principal.Name)
	}
}

// TestGitConfigHomeFallbackIgnoredWhenXDGSet pins that $HOME/.config/git/config
// is only git's fallback for an unset XDG_CONFIG_HOME. When XDG_CONFIG_HOME is
// set, a conflicting identity in the fallback file must not be read, or a stale
// identity there would attribute the action to the wrong person.
func TestGitConfigHomeFallbackIgnoredWhenXDGSet(t *testing.T) {
	home := t.TempDir()
	xdg := t.TempDir()
	xdgDir := filepath.Join(xdg, "git")
	if err := os.MkdirAll(xdgDir, 0o750); err != nil {
		t.Fatalf("create xdg dir: %v", err)
	}
	fallbackDir := filepath.Join(home, ".config", "git")
	if err := os.MkdirAll(fallbackDir, 0o750); err != nil {
		t.Fatalf("create fallback dir: %v", err)
	}
	writeIdentityFile(t, filepath.Join(xdgDir, "config"),
		"[user]\n\tname = XDG Person\n\temail = xdg@goodkind.io\n")
	writeIdentityFile(t, filepath.Join(fallbackDir, "config"),
		"[user]\n\tname = Stale Person\n\temail = stale@goodkind.io\n")

	t.Setenv("GIT_CONFIG_GLOBAL", "")
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("HOME", home)

	principal, err := GitConfigOperatorSource{}.Resolve(t.Context())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if principal.Email != "xdg@goodkind.io" {
		t.Fatalf("email = %q, want the fallback file ignored", principal.Email)
	}
	if principal.Name != "XDG Person" {
		t.Fatalf("name = %q, want the fallback file ignored", principal.Name)
	}
}

func writeIdentityFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
