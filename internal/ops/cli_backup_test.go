package ops

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"

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

func TestBackupHelpSubcommand(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "backup", args: []string{"help"}, want: "Subcommands initialize an object-store bucket"},
		{name: "subcommand", args: []string{"help", "buckets-init"}, want: "Idempotently create the SeaweedFS S3 backup bucket"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := &cobra.Command{Use: "tack"}
			ops := &cobra.Command{Use: "ops"}
			ops.AddCommand(backupCommand(&cli.Factory{}))
			root.AddCommand(ops)
			var output bytes.Buffer
			root.SetOut(&output)
			root.SetErr(&output)
			root.SetArgs(append([]string{"ops", "backup"}, test.args...))

			if err := root.Execute(); err != nil {
				t.Fatalf("ops backup %s: %v", strings.Join(test.args, " "), err)
			}
			if !strings.Contains(output.String(), test.want) {
				t.Fatalf("output missing %q: %s", test.want, output.String())
			}
		})
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
