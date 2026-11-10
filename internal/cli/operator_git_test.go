package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

func TestGitConfigOperatorSourceReadsHomeConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", "")
	writeGitConfig(t, filepath.Join(home, ".gitconfig"), "Ada Lovelace", "ada@example.com")

	principal, err := (GitConfigOperatorSource{}).Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if principal.Name != "Ada Lovelace" || principal.Email != "ada@example.com" {
		t.Fatalf("principal = %+v", principal)
	}
	if principal.Source != "git" {
		t.Fatalf("source = %q, want git", principal.Source)
	}
	if principal.ID.Version() != uuid.Version(5) {
		t.Fatalf("id version = %v, want 5", principal.ID.Version())
	}
}

func TestGitConfigOperatorSourceRequiresEmail(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", "")
	writeGitConfig(t, filepath.Join(home, ".gitconfig"), "Ada Lovelace", "")

	_, err := (GitConfigOperatorSource{}).Resolve(context.Background())
	if err == nil || !strings.Contains(err.Error(), "user.email") {
		t.Fatalf("Resolve error = %v, want user.email error", err)
	}
}

func TestGitConfigOperatorSourceHonorsGlobalOverride(t *testing.T) {
	home := t.TempDir()
	global := t.TempDir()
	t.Setenv("HOME", home)
	globalPath := filepath.Join(global, "global.gitconfig")
	t.Setenv("GIT_CONFIG_GLOBAL", globalPath)
	writeGitConfig(t, filepath.Join(home, ".gitconfig"), "Home User", "home@example.com")
	writeGitConfig(t, globalPath, "Global User", "global@example.com")

	principal, err := (GitConfigOperatorSource{}).Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if principal.Name != "Global User" || principal.Email != "global@example.com" {
		t.Fatalf("principal = %+v", principal)
	}
}

func TestGitConfigOperatorSourceUsesStableID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", "")
	writeGitConfig(t, filepath.Join(home, ".gitconfig"), "Ada Lovelace", "ada@example.com")
	source := GitConfigOperatorSource{}

	first, err := source.Resolve(context.Background())
	if err != nil {
		t.Fatalf("first Resolve: %v", err)
	}
	second, err := source.Resolve(context.Background())
	if err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("ids differ: %s and %s", first.ID, second.ID)
	}
}

// TestGitConfigQuotedEmailYieldsSameID pins that quoting an email in the git
// config does not change the operator's identity. The actor id is derived from
// the email, so a stray pair of quotes would otherwise split one person into
// two operators in the ledger.
func TestGitConfigQuotedEmailYieldsSameID(t *testing.T) {
	plain := resolveGitOperator(t, "[user]\n\tname = Alex\n\temail = alex@goodkind.io\n")
	quoted := resolveGitOperator(t, "[user]\n\tname = \"Alex\"\n\temail = \"alex@goodkind.io\"\n")
	if plain.ID != quoted.ID {
		t.Fatalf("quoted email id = %s, plain = %s", quoted.ID, plain.ID)
	}
	if quoted.Email != "alex@goodkind.io" {
		t.Fatalf("quoted email = %q, want unquoted", quoted.Email)
	}
	if quoted.Name != "Alex" {
		t.Fatalf("quoted name = %q, want unquoted", quoted.Name)
	}
}

// TestGitConfigTrailingCommentIsStripped pins that a comment after a value is
// not folded into the identity.
func TestGitConfigTrailingCommentIsStripped(t *testing.T) {
	principal := resolveGitOperator(t, "[user]\n\temail = alex@goodkind.io ; work\n")
	if principal.Email != "alex@goodkind.io" {
		t.Fatalf("email = %q, want the comment stripped", principal.Email)
	}
}

func writeGitConfig(t *testing.T, path, name, email string) {
	t.Helper()
	content := "[user]\n\tname = " + name + "\n"
	if email != "" {
		content += "\temail = " + email + "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write gitconfig: %v", err)
	}
}

func resolveGitOperator(t *testing.T, contents string) audit.OperatorPrincipal {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "gitconfig")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write gitconfig: %v", err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", path)
	principal, err := GitConfigOperatorSource{}.Resolve(t.Context())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return principal
}
