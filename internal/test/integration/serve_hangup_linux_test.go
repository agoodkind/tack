package integration_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/testenv"
)

const (
	serveStartedRecord = "starting server"
	// serveStartLimit limits the wait for serve to build its runtime graph
	// and log starting server.
	serveStartLimit = 2 * time.Minute
	// serveExitLimit limits the wait for serve to exit after terminal hangup.
	serveExitLimit = time.Minute
)

// TestServeExitsOnHangup checks that serve --execute ends by SIGHUP after
// its controlling terminal closes and does not log cli.hangup_ignored.
func TestServeExitsOnHangup(t *testing.T) {
	ledger := testenv.Ledger(t)
	logs := t.TempDir()
	serverLog := filepath.Join(logs, "tack.jsonl")
	environment := append(os.Environ(), "DATABASE_URL="+ledger, "FDB_CLUSTER_FILE="+testenv.FreshFoundationDB(t),
		"ENV=development", "PORT=0", "AUDIT_ALLOW_UNRECORDED=true", "AUDIT_OPERATOR_DSN="+ledger,
		"LOG_DISABLE_STDOUT=true", "LOG_JSON_FILE="+serverLog, "LOG_TEXT_FILE="+filepath.Join(logs, "tack.log"))
	process := startInTerminal(t, buildMigrateServer(t), environment, "--execute",
		"--operator-id", uuid.Must(uuid.NewV7()).String(), "--operator-email", hangupOperatorEmail, "serve")
	process.waitForLogRecord(t, serverLog, serveStartedRecord, serveStartLimit)
	process.hangUp(t)
	status := process.waitForExit(t, serveExitLimit)
	if !status.Signaled() || status.Signal() != syscall.SIGHUP {
		t.Fatalf("serve exited with wait status %v after the terminal hangup, want an exit by SIGHUP:\n%s", status, process.output)
	}
	if logContainsRecord(t, serverLog, hangupIgnoredRecord) {
		t.Fatalf("the server log %s contains %s for serve --execute", serverLog, hangupIgnoredRecord)
	}
}
