package integration_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/testenv"
)

// operatorReadRow is the action, outcome, and error code of one ledger row of
// an operator.
type operatorReadRow struct {
	Action  string
	Outcome string
	Code    string
}

// lastChangeCommand runs `audit last-change` for nodeID through the built
// server as operatorID, with readerDSN as AUDIT_READER_DSN. It returns stdout,
// stderr, and the error of the process.
func lastChangeCommand(t *testing.T, readerDSN string, operatorID, orgID, nodeID uuid.UUID, oldest, latest time.Time) (string, string, error) {
	t.Helper()
	ledgerDSN := testenv.Ledger(t)
	logs := t.TempDir()
	process := exec.CommandContext(t.Context(), buildMigrateServer(t),
		"--execute", "--operator-id", operatorID.String(), "--operator-email", "last-change@example.test",
		"--output", "json", "audit", "last-change", "--org", orgID.String(), "--entity-id", nodeID.String(),
		"--oldest", oldest.UTC().Format(time.RFC3339), "--latest", latest.UTC().Format(time.RFC3339))
	process.Env = append(os.Environ(),
		"DATABASE_URL="+ledgerDSN, "FDB_CLUSTER_FILE="+testenv.FoundationDB(t), "ENV=development",
		"AUDIT_OPERATOR_DSN="+ledgerDSN, "AUDIT_READER_DSN="+readerDSN, "LOG_DISABLE_STDOUT=true",
		"LOG_JSON_FILE="+filepath.Join(logs, "tack.json"), "LOG_TEXT_FILE="+filepath.Join(logs, "tack.log"))
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	process.Stdout, process.Stderr = stdout, stderr
	err := process.Run()
	return stdout.String(), stderr.String(), err
}

// runLastChange runs `audit last-change` for issueID through the built server
// with the audit_reader login of ledger and decodes the result.
func runLastChange(t *testing.T, ledger *nodeAuditLedger, orgID, issueID uuid.UUID, oldest, latest time.Time) lastChangeReport {
	t.Helper()
	stdout, stderr, err := lastChangeCommand(t, ledger.cfg.AuditReaderDSN, uuid.Must(uuid.NewV7()), orgID, issueID, oldest, latest)
	if err != nil {
		t.Fatalf("audit last-change failed: %v\n%s%s", err, stdout, stderr)
	}
	var envelope struct {
		Result lastChangeReport `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("decode the audit last-change output: %v\n%s", err, stdout)
	}
	return envelope.Result
}

// ledgerLoginDSN creates a LOGIN role that inherits only baseRole and returns
// the ledger admin DSN with the credentials of that role.
func ledgerLoginDSN(t *testing.T, ledger *nodeAuditLedger, baseRole string) string {
	t.Helper()
	secret := make([]byte, 16)
	if _, err := rand.Read(secret); err != nil {
		t.Fatalf("create the role secret: %v", err)
	}
	login := "tack_test_last_change_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	password := hex.EncodeToString(secret)
	statement := "CREATE ROLE " + login + " LOGIN INHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD '" + password + "' IN ROLE " + baseRole
	if _, err := ledger.admin.Exec(t.Context(), statement); err != nil {
		t.Fatalf("create %s in role %s: %v", login, baseRole, err)
	}
	t.Cleanup(func() {
		if _, err := ledger.admin.Exec(context.WithoutCancel(t.Context()), "DROP ROLE IF EXISTS "+login); err != nil {
			t.Errorf("drop the test login role %s: %v", login, err)
		}
	})
	parsed, err := url.Parse(testenv.Ledger(t))
	if err != nil {
		t.Fatalf("parse the ledger DSN: %v", err)
	}
	parsed.User = url.UserPassword(login, password)
	return parsed.String()
}

// waitForOperatorRows polls the ledger until it contains want rows with
// operatorID as the actor and returns them in event time order.
func waitForOperatorRows(t *testing.T, ledger *nodeAuditLedger, operatorID uuid.UUID, want int) []operatorReadRow {
	t.Helper()
	deadline := clock.Now().Add(nodeAuditDeadline)
	statement := readTestSQL(t, "last_change_operator_rows.sql")
	for {
		rows, err := ledger.admin.Query(t.Context(), statement, operatorID)
		if err != nil {
			t.Fatalf("read the ledger rows of operator %s: %v", operatorID, err)
		}
		var found []operatorReadRow
		for rows.Next() {
			var row operatorReadRow
			if err := rows.Scan(&row.Action, &row.Outcome, &row.Code); err != nil {
				rows.Close()
				t.Fatalf("scan a ledger row of operator %s: %v", operatorID, err)
			}
			found = append(found, row)
		}
		rows.Close()
		if len(found) >= want {
			return found
		}
		if clock.Now().After(deadline) {
			t.Fatalf("ledger rows of operator %s after %s = %+v, want %d", operatorID, nodeAuditDeadline, found, want)
		}
		time.Sleep(nodeAuditPollInterval)
	}
}
