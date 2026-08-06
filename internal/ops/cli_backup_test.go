package ops

import (
	"strings"
	"testing"

	"goodkind.io/tack/internal/cli"
)

// TestBareBackupCommandRefuses locks in that `ops backup` with no
// subcommand runs nothing and exits nonzero. The 2026-08-05 S0 was caused
// by the bare command silently running a full production snapshot.
func TestBareBackupCommandRefuses(t *testing.T) {
	cmd := backupCommand(&cli.Factory{})
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("bare `ops backup` must return an error, ran something instead")
	}
	if !strings.Contains(err.Error(), "subcommand") {
		t.Fatalf("error must direct the operator to a subcommand, got: %v", err)
	}
}

// TestBackupSubcommandsPresent locks the surviving subcommand set so a
// rename or accidental deletion fails loudly.
func TestBackupSubcommandsPresent(t *testing.T) {
	cmd := backupCommand(&cli.Factory{})
	want := map[string]bool{
		"buckets-init": false, "yb-pitr-init": false,
		"yb-snapshot-export": false, "restore-drill": false,
		"fdb-continuous-init": false,
	}
	for _, sub := range cmd.Commands() {
		name := strings.Fields(sub.Use)[0]
		if _, ok := want[name]; ok {
			want[name] = true
		}
		if name == "verify" {
			t.Fatal("verify subcommand must be deleted with the manifest machinery")
		}
	}
	for name, seen := range want {
		if !seen {
			t.Fatalf("subcommand %s missing", name)
		}
	}
}
