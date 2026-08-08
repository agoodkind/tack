package cli

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/audit"
)

func TestFlagOperatorSourceReturnsFlagValues(t *testing.T) {
	operatorID := uuid.New()
	operatorIDText := operatorID.String()
	factory := &Factory{
		operatorID:    &operatorIDText,
		operatorEmail: stringPointer("operator@example.com"),
		operatorName:  stringPointer("Operator User"),
	}

	principal, err := (FlagOperatorSource{Factory: factory}).Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := audit.OperatorPrincipal{
		ID: operatorID, Email: "operator@example.com", Name: "Operator User", Source: "flag",
	}
	if principal != want {
		t.Fatalf("principal = %+v, want %+v", principal, want)
	}
}

func TestFlagOperatorSourceRejectsInvalidID(t *testing.T) {
	operatorID := "not-a-uuid"
	factory := &Factory{operatorID: &operatorID}

	_, err := (FlagOperatorSource{Factory: factory}).Resolve(context.Background())
	if err == nil || !strings.Contains(err.Error(), "operator-id") {
		t.Fatalf("Resolve error = %v, want operator-id error", err)
	}
}

func TestNewOperatorSourcePrefersFlags(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", "")
	writeGitConfig(t, filepath.Join(home, ".gitconfig"), "Git User", "git@example.com")
	operatorID := uuid.New().String()
	factory := &Factory{operatorID: &operatorID}

	principal, err := NewOperatorSource(factory).Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if principal.Source != "flag" || principal.ID.String() != operatorID {
		t.Fatalf("principal = %+v", principal)
	}
}

// TestNewOperatorSourceReadsFlagsAtResolveTime pins the ordering that matters:
// the root command is constructed before cobra parses anything, so a source
// that chose its mechanism at construction would always see empty flags and
// silently ignore an operator who passed one.
func TestNewOperatorSourceReadsFlagsAtResolveTime(t *testing.T) {
	factory := &Factory{Cfg: nil, In: nil, Out: nil, Err: nil}
	root := &cobra.Command{Use: "tack"}
	factory.RegisterGlobalFlags(root)
	source := NewOperatorSource(factory)

	wantID := "019dd222-440e-729a-a442-281aaf73ca30"
	root.SetArgs([]string{"--operator-id", wantID, "--operator-email", "ops@goodkind.io"})
	if err := root.ParseFlags([]string{"--operator-id", wantID, "--operator-email", "ops@goodkind.io"}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}

	principal, err := source.Resolve(t.Context())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if principal.ID.String() != wantID {
		t.Fatalf("id = %s, want %s", principal.ID, wantID)
	}
	if principal.Source != "flag" {
		t.Fatalf("source = %q, want flag", principal.Source)
	}
}

func TestFactoryParsesOperatorFlags(t *testing.T) {
	operatorID := uuid.New()
	factory := &Factory{}
	root := &cobra.Command{Use: "tack", Run: func(*cobra.Command, []string) {}}
	factory.RegisterGlobalFlags(root)
	root.SetArgs([]string{
		"--operator-id", operatorID.String(),
		"--operator-email", "operator@example.com",
		"--operator-name", "Operator User",
		"--execute",
	})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	gotID, gotEmail, gotName := factory.Operator()
	if gotID != operatorID.String() || gotEmail != "operator@example.com" || gotName != "Operator User" {
		t.Fatalf("operator = %q, %q, %q", gotID, gotEmail, gotName)
	}
	if !factory.Execute() {
		t.Fatal("Execute = false, want true")
	}
}

func stringPointer(value string) *string {
	return &value
}
