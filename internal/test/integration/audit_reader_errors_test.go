package integration_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/testenv"
)

const (
	// readerErrorSecret is the password text that pgx does not mask in the malformed DSN.
	readerErrorSecret = "parts@secret"
	// readerMalformedDSN is a connection string that pgx cannot parse.
	readerMalformedDSN = "postgres://nobody:manifest@" + readerErrorSecret + "@[::1:1/tack"
	// readerFixedMessage is the fixed error text that tells the operator to check AUDIT_READER_DSN.
	readerFixedMessage = "check AUDIT_READER_DSN"
	// readerParseErrorText is the raw pgx parse error text.
	readerParseErrorText = "cannot parse"
	// readerConnectSecret is the password in the connection strings that pgx can parse.
	readerConnectSecret = "reader-connect-secret"
	// readerRefusedPortDSN is a valid connection string with a port that refuses connections.
	readerRefusedPortDSN = "postgres://nobody:" + readerConnectSecret + "@[::1]:1/tack?connect_timeout=1"
	// readerErrorOperatorKey is the operator email used for the test runs.
	readerErrorOperatorKey = "reader-errors@example.test"
)

// readerErrorCommand specifies the arguments, expected audit row count, and whether the rows must record the failure for one command.
type readerErrorCommand struct {
	args       []string
	rows       int
	storesFail bool
}

// readerErrorDSN specifies the name, connection string, required error text, and forbidden text for one test case.
type readerErrorDSN struct {
	name      string
	dsn       string
	required  string
	forbidden []string
}

// TestAuditReaderErrorsOmitConnectionStrings runs ops audit consumer-offsets, ops audit dlq inspect, and ops audit dlq replay through the rendered ops tree with a malformed DSN, a refused port, and a refused login. It checks the returned errors, standard output, standard error, logs, and stored audit rows for forbidden text.
func TestAuditReaderErrorsOmitConnectionStrings(t *testing.T) {
	ledger := startNodeAuditLedger(t)
	parsed, err := url.Parse(testenv.Ledger(t))
	if err != nil {
		t.Fatalf("parse the ledger DSN: %v", err)
	}
	parsed.User = url.UserPassword("nobody", readerConnectSecret)
	dsns := []readerErrorDSN{
		{name: "malformed", dsn: readerMalformedDSN, required: readerFixedMessage, forbidden: []string{readerErrorSecret, readerParseErrorText}},
		{name: "refused port", dsn: readerRefusedPortDSN, required: "", forbidden: []string{readerConnectSecret}},
		{name: "refused login", dsn: parsed.String(), required: "", forbidden: []string{readerConnectSecret}},
	}
	commands := []readerErrorCommand{
		{args: []string{"ops", "audit", "consumer-offsets"}, rows: 1, storesFail: false},
		{args: []string{"ops", "audit", "dlq", "inspect"}, rows: 1, storesFail: true},
		{args: []string{"ops", "audit", "dlq", "replay", "--limit", "1"}, rows: 2, storesFail: true},
	}
	for _, command := range commands {
		for _, current := range dsns {
			operatorID := uuid.Must(uuid.NewV7())
			output := runReaderErrorCommand(t, ledger, current, operatorID, command)
			requireReaderErrorRows(t, ledger, current, operatorID, command, output)
		}
	}
}

// runReaderErrorCommand runs one command through the rendered ops tree, checks its error, output, and logs, and returns their combined text.
func runReaderErrorCommand(t *testing.T, ledger *nodeAuditLedger, current readerErrorDSN, operatorID uuid.UUID, command readerErrorCommand) string {
	t.Helper()
	cfg := *ledger.cfg
	cfg.AuditReaderDSN = current.dsn
	stdout, stderr, logs := &bytes.Buffer{}, &bytes.Buffer{}, &bytes.Buffer{}
	factory := cli.System(&cfg)
	factory.Out, factory.Err = stdout, stderr
	factory.SetAuditOutbox(audit.NewPoolOutbox(ledger.admin))
	factory.SetAuditReads(audit.NewPoolReadLifecycle(rolePool(t, testenv.Ledger(t), "audit_operator")))
	root := nodeAuditCommandRoot(factory)
	root.SetContext(t.Context())
	root.SetArgs(slices.Concat([]string{
		"--execute", "--output", "json", "--operator-id", operatorID.String(), "--operator-email", readerErrorOperatorKey,
	}, command.args))
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	runErr := root.Execute()
	slog.SetDefault(previousLogger)
	if runErr == nil {
		t.Fatalf("%v with the %s DSN succeeded, want an error", command.args, current.name)
	}
	combined := strings.Join([]string{runErr.Error(), stdout.String(), stderr.String(), logs.String()}, "\n")
	for _, forbidden := range current.forbidden {
		if strings.Contains(combined, forbidden) {
			t.Fatalf("%v with the %s DSN exposed %q in output or logs:\n%s", command.args, current.name, forbidden, combined)
		}
	}
	if !strings.Contains(runErr.Error(), current.required) {
		t.Fatalf("%v with the %s DSN returned %q, want %q", command.args, current.name, runErr, current.required)
	}
	return combined
}

// requireReaderErrorRows polls the ledger until it finds the expected rows for the operator and checks their text.
func requireReaderErrorRows(t *testing.T, ledger *nodeAuditLedger, current readerErrorDSN, operatorID uuid.UUID, command readerErrorCommand, output string) {
	t.Helper()
	now := clock.Now()
	filter := audit.QueryFilter{
		OrgID: audit.SystemOrgID(), Oldest: now.Add(-time.Hour), Latest: now.Add(time.Hour),
		Action: "", ActorID: operatorID, EntityID: uuid.Nil, RequestID: "", TraceID: "", Limit: command.rows + 1,
	}
	deadline := clock.Now().Add(nodeAuditDeadline)
	for {
		rows, err := ledger.reader.Query(t.Context(), filter)
		if err != nil {
			t.Fatalf("query the rows of operator %s: %v", operatorID, err)
		}
		if len(rows) >= command.rows {
			requireReaderErrorRowText(t, current, command, rows)
			return
		}
		if clock.Now().After(deadline) {
			t.Fatalf("%v with the %s DSN stored %d rows after %s, want %d\n%s", command.args, current.name, len(rows), nodeAuditDeadline, command.rows, output)
		}
		time.Sleep(nodeAuditPollInterval)
	}
}

// requireReaderErrorRowText fails the test if a row contains forbidden text or does not record a required failure.
func requireReaderErrorRowText(t *testing.T, current readerErrorDSN, command readerErrorCommand, rows []audit.Row) {
	t.Helper()
	failureStored := false
	for _, row := range rows {
		encoded, err := json.Marshal(row)
		if err != nil {
			t.Fatalf("encode row %s: %v", row.EventID, err)
		}
		for _, forbidden := range current.forbidden {
			if strings.Contains(string(encoded), forbidden) {
				t.Fatalf("%v with the %s DSN stored %q in row %s:\n%s", command.args, current.name, forbidden, row.EventID, encoded)
			}
		}
		if row.Error != nil && strings.Contains(row.Error.Message, current.required) {
			failureStored = true
		}
	}
	if command.storesFail && !failureStored {
		t.Fatalf("%v with the %s DSN stored rows %+v, want a failure that contains %q", command.args, current.name, rows, current.required)
	}
}
