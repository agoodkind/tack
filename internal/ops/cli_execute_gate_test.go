package ops_test

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/ops"
)

// TestOperatorCommandsSetGlobalExecuteFlag checks that --execute sets the global value
// the audit mechanism reads. A local execute flag would override the global
// flag and prevent command execution.
func TestOperatorCommandsSetGlobalExecuteFlag(t *testing.T) {
	for _, path := range [][]string{
		{"ops", "repair", "reference-uniqueness"},
		{"ops", "qa", "datagen", "seed"},
		{"ops", "provision"},
	} {
		factory := &cli.Factory{Cfg: nil, In: nil, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
		root := executeGateRoot(t, factory)
		root.SetArgs(append(append([]string{}, path...), "--execute", "--help"))
		if err := root.Execute(); err != nil {
			t.Fatalf("%v --execute: %v", path, err)
		}

		if !factory.Execute() {
			t.Fatalf(
				"%v --execute did not set the global value; a local flag overrides it",
				path,
			)
		}
	}
}

// TestCommandFlagsExcludeGlobalFlagNames checks that commands do not declare local
// flags with global flag names. The factory reads only the global values.
func TestCommandFlagsExcludeGlobalFlagNames(t *testing.T) {
	factory := &cli.Factory{Cfg: nil, In: nil, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	root := executeGateRoot(t, factory)
	globals := map[string]bool{}
	root.PersistentFlags().VisitAll(func(flag *pflag.Flag) { globals[flag.Name] = true })

	var walk func(command *cobra.Command)
	walk = func(command *cobra.Command) {
		command.LocalNonPersistentFlags().VisitAll(func(flag *pflag.Flag) {
			if globals[flag.Name] {
				t.Errorf(
					"%s declares --%s, which is a global flag",
					command.CommandPath(),
					flag.Name,
				)
			}
		})

		for _, child := range command.Commands() {
			walk(child)
		}
	}
	walk(root)
}

func executeGateRoot(t *testing.T, factory *cli.Factory) *cobra.Command {
	t.Helper()
	registry := clispec.NewRegistry()
	ops.RegisterCommands(registry, factory)
	root := &cobra.Command{Use: "tack", SilenceErrors: true, SilenceUsage: true}
	factory.RegisterGlobalFlags(root)

	for _, rendered := range clispec.RenderCobra(registry, factory) {
		root.AddCommand(rendered)
	}

	return root
}
