//go:build runtimeaudit

package integration_test

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/ops"
)

// unreachableDatabaseURL prevents static help checks from connecting to a
// working SQL database.
const unreachableDatabaseURL = "postgres://coverage@unreachable.invalid:1/none?connect_timeout=1"

// requireStaticHelp runs a command with an unreachable SQL database and an
// absent FoundationDB cluster file. It requires the command to succeed and
// print usage text.
func requireStaticHelp(t *testing.T, key string) {
	t.Helper()
	cfg := &config.Config{
		DatabaseURL: unreachableDatabaseURL, AuditOperatorDSN: unreachableDatabaseURL,
		FDBClusterFile: filepath.Join(t.TempDir(), "absent.cluster"),
	}
	unreachable, err := pgxpool.New(t.Context(), unreachableDatabaseURL)
	if err != nil {
		t.Fatalf("The test could not create the database pool required for the static help check: %v", err)
	}
	t.Cleanup(unreachable.Close)
	factory := cli.System(cfg)
	var output bytes.Buffer
	factory.Out, factory.Err = &output, &output
	factory.SetAuditOutbox(audit.NewPoolOutbox(unreachable))
	factory.SetAuditReads(audit.NewPoolReadLifecycle(unreachable))
	registry := clispec.NewRegistry()
	ops.RegisterCommands(registry, factory)
	root := &cobra.Command{Use: "tack", SilenceErrors: true, SilenceUsage: true}
	factory.RegisterGlobalFlags(root)
	for _, rendered := range clispec.RenderCobra(registry, factory) {
		root.AddCommand(rendered)
	}
	factory.SetOperatorIdentitySource(cli.NewOperatorSource(factory))
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs(strings.Fields(strings.TrimPrefix(key, "command ")))
	if err := root.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("The command returned an error when static help should succeed without a database; the command, error, and output follow: %s: %v\n%s", key, err, output.String())
	}
	if !strings.Contains(output.String(), "Usage:") {
		t.Fatalf("The command output did not contain the expected Usage: text for %s:\n%s", key, output.String())
	}
}
