package integration_test

import (
	"bytes"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const (
	// strictSoakKinds is the number of operation kinds in the soak mix.
	strictSoakKinds = 15
	// strictSoakOperations is three passes of the soak operation mix.
	strictSoakOperations = 3 * strictSoakKinds
)

// TestNodeIntegrityStrictDatagenSoakAfterActivation verifies that all
// 45 soak operations succeed with strict validation active.
// It calls integration.SetupTestEnv to set a per-test FoundationDB key
// prefix and seed one organization. It calls activateStrict before
// runStrictSoak. The soak stops at its first failed operation. The test
// requires the max_ops stop reason and an operation count of 45.
func TestNodeIntegrityStrictDatagenSoakAfterActivation(t *testing.T) {
	integration.SetupTestEnv(t)
	activateStrict(t)

	result := runStrictSoak(t)
	if result.StopReason != "max_ops" || result.Operations != strictSoakOperations {
		t.Fatalf("soak after activation stopped with %q after %d operations, want max_ops after %d",
			result.StopReason, result.Operations, strictSoakOperations)
	}
}

// strictSoakResult contains the soak result fields that the test reads.
type strictSoakResult struct {
	StopReason string `json:"stop_reason"`
	Operations int    `json:"operations"`
}

// runStrictSoak loads the server configuration for the test engines and
// sets DatagenAllowTarget to local. It disables public search, the audit
// writer, and Kafka. It runs ops qa datagen soak --commit once through the
// audited command tree with the real SQL outbox for three passes of the
// operation mix. Each search operation counts as unavailable and does not
// fail when public search is off. It returns the decoded stop reason and
// operation count.
func runStrictSoak(t *testing.T) strictSoakResult {
	t.Helper()
	ledgerDSN := testenv.Ledger(t)
	t.Setenv("DATABASE_URL", ledgerDSN)
	t.Setenv("FDB_CLUSTER_FILE", testenv.FoundationDB(t))
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	cfg.DatagenAllowTarget = "local"
	cfg.AuditKafkaBrokers = ""
	cfg.AuditWriterDSN = ""
	cfg.AuditAllowUnrecorded = true
	cfg.SearchPublicEnabled = false
	pool, err := pgxpool.New(t.Context(), ledgerDSN)
	if err != nil {
		t.Fatalf("open the outbox pool: %v", err)
	}
	t.Cleanup(pool.Close)
	output := &bytes.Buffer{}
	factory := cli.System(cfg)
	factory.Out = output
	factory.SetAuditOutbox(audit.NewPoolOutbox(pool))
	t.Cleanup(factory.CloseAuditOutbox)
	root := nodeAuditCommandRoot(factory)
	root.SetArgs([]string{
		"--execute", "--output", "json", "--operator-id", "019dd226-440e-729a-a442-281aaf73ca34",
		"--operator-email", "operator@example.com", "--operator-name", "Strict Soak Test",
		"ops", "qa", "datagen", "soak", "--commit", "--duration", "10m", "--rate", "1000",
		"--max-ops", strconv.Itoa(strictSoakOperations), "--seed", strconv.FormatInt(clock.Now().UnixNano(), 10),
	})
	if err := root.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("ops qa datagen soak --commit after activation: %v\n%s", err, output.String())
	}
	var decoded struct {
		Result strictSoakResult `json:"result"`
	}
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatalf("decode the soak output %q: %v", output.String(), err)
	}
	return decoded.Result
}
