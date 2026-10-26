package integration_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

// lastChangeRun is one `audit last-change` process: its stdout, its stderr,
// its text log, and its exit error.
type lastChangeRun struct {
	stdout string
	stderr string
	log    string
	err    error
}

// output returns the captured stdout, stderr, and text log for a failure
// message.
func (run lastChangeRun) output() string {
	return "stdout:\n" + run.stdout + "\nstderr:\n" + run.stderr + "\nlog:\n" + run.log
}

// readerDSNVariables lists the environment variables that can set the
// reader DSN, directly or through a secret file reference.
var readerDSNVariables = []string{"AUDIT_READER_DSN", "AUDIT_READER_DSN_FILE"}

// lastChangeCommand runs `audit last-change` for nodeID through the built
// server as operatorID. It removes every reader DSN variable that the test
// process inherits and sets AUDIT_READER_DSN only when readerDSN is not
// empty. The command logs its error to the text log, and LOG_DISABLE_STDOUT
// keeps log lines out of stdout.
func lastChangeCommand(t *testing.T, readerDSN string, operatorID, orgID, nodeID uuid.UUID, oldest, latest time.Time) lastChangeRun {
	t.Helper()
	ledgerDSN := testenv.Ledger(t)
	logs := t.TempDir()
	textLog := filepath.Join(logs, "tack.log")
	process := exec.CommandContext(t.Context(), buildMigrateServer(t),
		"--execute", "--operator-id", operatorID.String(), "--operator-email", "last-change@example.test",
		"--output", "json", "audit", "last-change", "--org", orgID.String(), "--entity-id", nodeID.String(),
		"--oldest", oldest.UTC().Format(time.RFC3339), "--latest", latest.UTC().Format(time.RFC3339))
	environment := make([]string, 0, len(os.Environ())+8)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if !slices.Contains(readerDSNVariables, name) {
			environment = append(environment, entry)
		}
	}
	environment = append(environment,
		"DATABASE_URL="+ledgerDSN, "FDB_CLUSTER_FILE="+testenv.FoundationDB(t), "ENV=development",
		"AUDIT_OPERATOR_DSN="+ledgerDSN, "LOG_DISABLE_STDOUT=true",
		"LOG_JSON_FILE="+filepath.Join(logs, "tack.json"), "LOG_TEXT_FILE="+textLog)
	if readerDSN != "" {
		environment = append(environment, "AUDIT_READER_DSN="+readerDSN)
	}
	process.Env = environment
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	process.Stdout, process.Stderr = stdout, stderr
	err := process.Run()
	logged, readErr := os.ReadFile(textLog)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		t.Fatalf("read the audit last-change log %s: %v", textLog, readErr)
	}
	return lastChangeRun{stdout: stdout.String(), stderr: stderr.String(), log: string(logged), err: err}
}

// runLastChange runs `audit last-change` for issueID through the built server
// with the audit_reader login of ledger and decodes the result.
func runLastChange(t *testing.T, ledger *nodeAuditLedger, orgID, issueID uuid.UUID, oldest, latest time.Time) lastChangeReport {
	t.Helper()
	run := lastChangeCommand(t, ledger.cfg.AuditReaderDSN, uuid.Must(uuid.NewV7()), orgID, issueID, oldest, latest)
	if run.err != nil {
		t.Fatalf("audit last-change failed: %v\n%s", run.err, run.output())
	}
	var envelope struct {
		Result lastChangeReport `json:"result"`
	}
	if err := json.Unmarshal([]byte(run.stdout), &envelope); err != nil {
		t.Fatalf("decode the audit last-change output: %v\n%s", err, run.output())
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
// operatorID as the actor and returns them in event time order. Each failure
// message ends with commandOutput, the captured output of the command run.
func waitForOperatorRows(t *testing.T, ledger *nodeAuditLedger, operatorID uuid.UUID, want int, commandOutput string) []operatorReadRow {
	t.Helper()
	deadline := clock.Now().Add(nodeAuditDeadline)
	statement := readTestSQL(t, "last_change_operator_rows.sql")
	for {
		rows, err := ledger.admin.Query(t.Context(), statement, operatorID)
		if err != nil {
			t.Fatalf("read the ledger rows of operator %s: %v\n%s", operatorID, err, commandOutput)
		}
		var found []operatorReadRow
		for rows.Next() {
			var row operatorReadRow
			if err := rows.Scan(&row.Action, &row.Outcome, &row.Code); err != nil {
				rows.Close()
				t.Fatalf("scan a ledger row of operator %s: %v\n%s", operatorID, err, commandOutput)
			}
			found = append(found, row)
		}
		rows.Close()
		if len(found) >= want {
			return found
		}
		if clock.Now().After(deadline) {
			t.Fatalf("ledger rows of operator %s after %s = %+v, want %d\n%s", operatorID, nodeAuditDeadline, found, want, commandOutput)
		}
		time.Sleep(nodeAuditPollInterval)
	}
}
