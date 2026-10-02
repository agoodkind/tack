package integration

import (
	"encoding/json"
	"strings"
	"testing"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/testenv"
)

const (
	// bootstrapLedgerContainer is the fixed container that `ops ledger
	// node-prepare` and `ops ledger node-wait` act on.
	bootstrapLedgerContainer = "tack-yugabyte-1"
	// bootstrapOperatorID is the test operator, never a person.
	bootstrapOperatorID = "019dd226-440e-729a-a442-281aaf73ca31"
	// bootstrapClusterDirectoryVariable is the host directory the fdbcli
	// one-shot mounts.
	bootstrapClusterDirectoryVariable = "TACK_OPS_FDB_CLUSTER_DIR"
)

// nodePrepareOutput is the part of the node-prepare JSON result this test
// reads.
type nodePrepareOutput struct {
	Result struct {
		State   string `json:"state"`
		Before  string `json:"before"`
		After   string `json:"after"`
		Stopped bool   `json:"stopped"`
	} `json:"result"`
}

// TestLedgerBootstrapFromEmpty starts an unmigrated ledger node named
// tack-yugabyte-1 and runs the ledger node commands through the audited
// command tree with the real operator outbox. Before `ops provision`,
// node-prepare fails because the operator login cannot record its intent.
// After provision, node-prepare reports the node absent or unchanged and
// node-wait reports it healthy. The real relay and consumer then project the
// provision and node-prepare intent and outcome pairs and the node-wait read
// row into audit.events.
func TestLedgerBootstrapFromEmpty(t *testing.T) {
	node := testenv.StartEmptyLedger(t, bootstrapLedgerContainer)
	t.Setenv(bootstrapClusterDirectoryVariable, bootstrapClusterDirectory(t, testenv.FoundationDB(t)))
	cfg := bootstrapConfig(t, node)
	admin := openBootstrapPool(t, node.DSN)
	operator := openBootstrapPool(t, operatorLoginDSN(t, node.DSN, cfg.AuditOperatorPassword))
	run := bootstrapCommandRunner(t, cfg, operator)

	red := t.Run("node-prepare before provision fails", func(t *testing.T) {
		output, err := run("ops", "ledger", "node-prepare")
		if err == nil || !strings.Contains(err.Error(), "record command intent") {
			t.Fatalf("node-prepare before provision = %v, want a record command intent error\n%s", err, output)
		}
	})
	if !red {
		t.FailNow()
	}
	t.Run("node commands succeed and record after provision", func(t *testing.T) {
		if output, err := run("ops", "provision"); err != nil {
			t.Fatalf("ops provision: %v\n%s", err, output)
		}
		output, err := run("ops", "ledger", "node-prepare")
		if err != nil {
			t.Fatalf("node-prepare after provision: %v\n%s", err, output)
		}
		requireNodePrepareKeptNode(t, output)
		if output, err := run("ops", "ledger", "node-wait", "--stall", "3m", "--poll", "2s"); err != nil {
			t.Fatalf("node-wait after provision: %v\n%s", err, output)
		}

		startBootstrapAuditPipeline(t, testenv.Kafka(t), node.DSN, admin)
		rows := waitForBootstrapRows(t, admin)
		requireBootstrapIntentAndOutcome(t, rows, audit.VerbOpsProvision)
		requireBootstrapIntentAndOutcome(t, rows, audit.VerbOpsLedgerNodePrepare)
		requireBootstrapReadRow(t, rows, audit.VerbOpsLedgerNodeWait)
	})
}

// requireNodePrepareKeptNode requires node-prepare to report the node absent
// or unchanged and to leave it running, so node-wait reads the same node.
func requireNodePrepareKeptNode(t *testing.T, output string) {
	t.Helper()
	var decoded nodePrepareOutput
	if err := json.Unmarshal([]byte(output), &decoded); err != nil {
		t.Fatalf("decode the node-prepare output %q: %v", output, err)
	}
	state := decoded.Result.State
	if (state != "absent" && state != "unchanged") || decoded.Result.Stopped {
		t.Fatalf("node-prepare state = %s (before %q, after %q, stopped %t), want absent or unchanged and running",
			state, decoded.Result.Before, decoded.Result.After, decoded.Result.Stopped)
	}
}
