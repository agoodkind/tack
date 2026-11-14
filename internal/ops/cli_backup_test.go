package ops

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
)

type backupTestOutbox struct{}

func (backupTestOutbox) WriteOutbox(context.Context, audit.Event) error { return nil }

type backupTestSource struct{}

func (backupTestSource) Resolve(context.Context) (audit.OperatorPrincipal, error) {
	return audit.OperatorPrincipal{
		ID:     uuid.MustParse("019dd226-440e-729a-a442-281aaf73ca30"),
		Email:  "operator@example.com",
		Name:   "Operator User",
		Source: "test",
	}, nil
}

// TestRestoreDrillRefusesUnparseableTargetTime checks that the command validates
// --fdb-target-time before running the drill. The factory does not provide a
// Docker daemon or object store. Drill execution would return a different error.
func TestRestoreDrillRefusesUnparseableTargetTime(t *testing.T) {
	factory := &cli.Factory{}
	factory.SetOperatorIdentitySource(backupTestSource{})
	factory.SetAuditOutbox(backupTestOutbox{})
	cmd := backupCommand(factory)
	factory.RegisterGlobalFlags(cmd)
	cmd.SetArgs([]string{"restore-drill", "--fdb-target-time", "yesterday", "--execute"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("an unparseable --fdb-target-time must stop the drill")
	}

	if !strings.Contains(err.Error(), "is not a time") {
		t.Fatalf("error must explain the target time was rejected, got: %v", err)
	}
}

func TestBackupHelpSubcommand(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "backup",
			args: []string{"help"},
			want: "Subcommands initialize an object-store bucket",
		},
		{
			name: "subcommand",
			args: []string{"help", "buckets-init"},
			want: "Idempotently create the SeaweedFS S3 backup bucket",
		},
	}

	for _, test := range tests {
		t.Run(
			test.name,
			func(t *testing.T) {
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
			},
		)
	}
}

// TestBackupSubcommandsPresent locks the surviving subcommand set so a
// rename or accidental deletion fails loudly.
func TestBackupSubcommandsPresent(t *testing.T) {
	cmd := backupCommand(&cli.Factory{})
	want := map[string]bool{
		"buckets-init":        false,
		"yb-pitr-init":        false,
		"yb-snapshot-export":  false,
		"yb-archive-node":     false,
		"restore-drill":       false,
		"fdb-continuous-init": false,
		"staleness-check":     false,
		"restore-version":     false,
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
