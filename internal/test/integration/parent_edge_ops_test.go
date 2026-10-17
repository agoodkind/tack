package integration_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/ops"
)

// runOps runs one audited ops command through the rendered Cobra tree with
// JSON output. It returns the result object of the first JSON value that the
// command writes, and the command error.
func (f parentEdgeFixture) runOps(t *testing.T, args ...string) (json.RawMessage, error) {
	t.Helper()
	var output bytes.Buffer
	factory := cli.System(f.cfg)
	factory.Out = &output
	pool, err := pgxpool.New(t.Context(), f.cfg.DatabaseURL)
	if err != nil {
		t.Fatalf("open audit ledger pool: %v", err)
	}
	t.Cleanup(pool.Close)
	factory.SetAuditOutbox(audit.NewPoolOutbox(pool))
	t.Cleanup(factory.CloseAuditOutbox)
	registry := clispec.NewRegistry()
	ops.RegisterCommands(registry, factory)
	root := &cobra.Command{Use: "tack", SilenceErrors: true, SilenceUsage: true}
	factory.RegisterGlobalFlags(root)
	for _, command := range clispec.RenderCobra(registry, factory) {
		root.AddCommand(command)
	}
	factory.SetOperatorIdentitySource(cli.NewOperatorSource(factory))
	root.SetArgs(append([]string{
		"--execute", "--output", "json", "--operator-id", "019dd226-440e-729a-a442-281aaf73ca30",
		"--operator-email", "operator@example.com", "--operator-name", "Parent Edge Test",
	}, args...))
	runErr := root.ExecuteContext(t.Context())
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(&output).Decode(&envelope); err != nil || len(envelope.Result) == 0 {
		t.Fatalf("decode the result of %v (command error %v): %v", args, runErr, err)
	}
	return envelope.Result, runErr
}
