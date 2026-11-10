package ops_test

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/ops"
)

// TestReferenceRepairIsReachableFromTheCommandLine pins that an operator can
// actually run the reference repair. Registering the operation is not enough on
// its own: the batch renderer skips every name beginning with "repair.", so the
// operation existed with no command able to invoke it and the production repair
// could not be run at all.
func TestReferenceRepairIsReachableFromTheCommandLine(t *testing.T) {
	command := findCommand(t, "ops", "repair", "reference-uniqueness")

	// The repair applies through the global --execute the audit choke-point
	// reads. Declaring a flag of that name here would take the name over and
	// leave the choke-point reading false forever, which is what
	// TestExecuteReachesTheAuditGate covers.
	if command.LocalNonPersistentFlags().Lookup("execute") != nil {
		t.Fatal("the repair declares its own --execute, which shadows the audit gate")
	}

	keep := command.Flags().Lookup("keep")
	if keep == nil {
		t.Fatal("the repair has no --keep flag, so an operator could not choose which node keeps a contested reference")
	}
	if keep.DefValue != "oldest" {
		t.Fatalf("--keep default = %q, want oldest", keep.DefValue)
	}
}

// findCommand walks the rendered command tree the server builds, so the test
// fails if the operation is dropped from RegisterCommands or renamed.
func findCommand(t *testing.T, path ...string) *cobra.Command {
	t.Helper()
	registry := clispec.NewRegistry()
	factory := &cli.Factory{Cfg: nil, In: nil, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	ops.RegisterCommands(registry, factory)

	root := &cobra.Command{Use: "tack"}
	for _, rendered := range clispec.RenderCobra(registry, factory) {
		root.AddCommand(rendered)
	}
	current := root
	for _, segment := range path {
		next, _, err := current.Find([]string{segment})
		if err != nil || next == current {
			t.Fatalf("command %q not found under %q", segment, current.Name())
		}
		current = next
	}
	return current
}
