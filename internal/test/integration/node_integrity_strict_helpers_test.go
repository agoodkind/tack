package integration_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/testenv"
)

// strictValidationOutput is the JSON result of `ops verify strict-validation`.
type strictValidationOutput struct {
	DryRun bool                       `json:"dry_run"`
	Result ops.StrictValidationReport `json:"result"`
}

// runStrictValidation runs `ops verify strict-validation` through the audited
// command tree against the test engines. When execute is true, it adds
// --execute. It returns the decoded report and the command error.
func runStrictValidation(t *testing.T, execute bool) (strictValidationOutput, error) {
	t.Helper()
	ledgerDSN := testenv.Ledger(t)
	t.Setenv("DATABASE_URL", ledgerDSN)
	t.Setenv("FDB_CLUSTER_FILE", testenv.FoundationDB(t))
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	pool, err := pgxpool.New(t.Context(), ledgerDSN)
	if err != nil {
		t.Fatalf("open the ledger pool: %v", err)
	}
	t.Cleanup(pool.Close)
	output := &bytes.Buffer{}
	factory := cli.System(cfg)
	factory.Out = output
	factory.SetAuditOutbox(audit.NewPoolOutbox(pool))
	t.Cleanup(factory.CloseAuditOutbox)
	args := []string{
		"--output", "json", "--operator-id", "019dd226-440e-729a-a442-281aaf73ca32",
		"--operator-email", "operator@example.com", "--operator-name", "Strict Validation Test",
		"ops", "verify", "strict-validation",
	}
	if execute {
		args = append([]string{"--execute"}, args...)
	}
	root := nodeAuditCommandRoot(factory)
	root.SetContext(t.Context())
	root.SetArgs(args)
	runErr := root.Execute()
	var decoded strictValidationOutput
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatalf("decode the strict validation output %q (command error %v): %v", output.String(), runErr, err)
	}
	return decoded, runErr
}
