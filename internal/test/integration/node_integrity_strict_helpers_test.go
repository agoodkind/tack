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
	"goodkind.io/tack/internal/response"
	"goodkind.io/tack/internal/testenv"
)

func runStrictValidation(t *testing.T, execute bool, flags ...string) (ops.StrictValidationOutput, error) {
	t.Helper()
	output, runErr := runStrictValidationOutput(t, execute, flags...)
	var document response.JSONDocument
	if err := json.Unmarshal(output, &document); err != nil {
		t.Fatalf("decode the strict validation output %q (command error %v): %v", output, runErr, err)
	}
	var decoded ops.StrictValidationOutput
	if err := json.Unmarshal(document.Result, &decoded); err != nil {
		t.Fatalf("decode the strict validation result %s (command error %v): %v", document.Result, runErr, err)
	}
	return decoded, runErr
}

// runStrictValidationOutput runs the audited command with flags after the
// command name and returns its stdout and error.
func runStrictValidationOutput(t *testing.T, execute bool, flags ...string) ([]byte, error) {
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
	factory.SetAuditReads(audit.NewPoolReadLifecycle(pool))
	t.Cleanup(factory.CloseAuditOutbox)
	args := []string{
		"--output", "json", "--operator-id", "019dd226-440e-729a-a442-281aaf73ca32",
		"--operator-email", "operator@example.com", "--operator-name", "Strict Validation Test",
		"ops", "verify", "strict-validation",
	}
	args = append(args, flags...)
	if execute {
		args = append([]string{"--execute"}, args...)
	}
	root := nodeAuditCommandRoot(factory)
	root.SetContext(t.Context())
	root.SetArgs(args)
	runErr := root.Execute()
	return output.Bytes(), runErr
}
