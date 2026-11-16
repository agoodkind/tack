package ops_test

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/ops"
)

// TestReferenceRepairCommandRegistersExecutionAndKeepFlags checks that the rendered
// command tree exposes the repair. The batch renderer skips repair.* operations.
func TestReferenceRepairCommandRegistersExecutionAndKeepFlags(t *testing.T) {
	command := findCommand(t, "ops", "repair", "reference-uniqueness")

	// The audit mechanism reads the global --execute flag. A local flag
	// would override it and leave the global value false.
	if command.LocalNonPersistentFlags().Lookup("execute") != nil {
		t.Fatal("the repair declares its own --execute, which shadows the audit gate")
	}

	keep := command.Flags().Lookup("keep")
	if keep == nil {
		t.Fatal(
			"the repair does not provide --keep for selecting the node that retains a contested reference",
		)
	}

	if keep.DefValue != "oldest" {
		t.Fatalf("--keep default = %q, want oldest", keep.DefValue)
	}
}

// findCommand searches the server's rendered command tree. It fails if
// RegisterCommands omits the operation or changes its name.
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
