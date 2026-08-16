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

// TestExecuteReachesTheAuditGate pins that --execute on any operator command
// reaches the audit choke-point, which is the switch that decides whether the
// command runs at all. A command that declares its own flag named execute
// shadows the global one: cobra binds the local flag, the choke-point still
// reads false, and the command silently prints its dry run forever. The
// reference repair shipped that way, so it could not be applied from the
// command line at all.
func TestExecuteReachesTheAuditGate(t *testing.T) {
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
			t.Fatalf("%v --execute never reached the audit gate; a local flag shadows the global one", path)
		}
	}
}

// TestNoCommandShadowsAGlobalFlag closes the class rather than the instance: a
// command whose own flag repeats a global name takes that name over, and the
// value the operator typed stops reaching the factory the whole CLI reads.
func TestNoCommandShadowsAGlobalFlag(t *testing.T) {
	factory := &cli.Factory{Cfg: nil, In: nil, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	root := executeGateRoot(t, factory)
	globals := map[string]bool{}
	root.PersistentFlags().VisitAll(func(flag *pflag.Flag) { globals[flag.Name] = true })

	var walk func(command *cobra.Command)
	walk = func(command *cobra.Command) {
		command.LocalNonPersistentFlags().VisitAll(func(flag *pflag.Flag) {
			if globals[flag.Name] {
				t.Errorf("%s declares --%s, which is a global flag", command.CommandPath(), flag.Name)
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
