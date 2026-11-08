package integration_test

import (
	"bytes"
	"log/slog"
	"net/url"
	"path/filepath"
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
	// readerErrorSecret is the password text that pgx does not mask in the
	// malformed DSN.
	readerErrorSecret = "parts@secret"
	// readerMalformedDSN is a connection string that pgx cannot parse.
	readerMalformedDSN = "postgres://nobody:manifest@" + readerErrorSecret + "@[::1:1/tack"
	// readerParseFailure is the exact text audit.NewReader returns when pgx
	// cannot parse a connection string. The text omits every setting because
	// the restore drill builds its own connection string.
	readerParseFailure = "audit: the reader connection string does not parse"
	// readerFixedMessage is the text a caller returns after it reads
	// AUDIT_READER_DSN and wraps readerParseFailure with the variable name.
	readerFixedMessage = "AUDIT_READER_DSN: " + readerParseFailure
	// readerParseErrorText is the raw pgx parse error text.
	readerParseErrorText = "cannot parse"
	// readerConnectFailure is the fixed text for a failed reader connection.
	readerConnectFailure = "audit: the reader did not connect"
	// readerConnectSecret is the password in the connection strings that pgx
	// can parse.
	readerConnectSecret = "reader-connect-secret"
	// readerRefusedPortDSN is a valid connection string with a port that
	// refuses connections.
	readerRefusedPortDSN = "postgres://nobody:" + readerConnectSecret + "@[::1]:1/tack?connect_timeout=1"
	// readerSlashPasswordDSN is a connection string with a slash in the
	// password that pgx parses without an error.
	readerSlashPasswordDSN = "postgres://nobody:1/" + readerConnectSecret + "@[::1]:1/tack"
	// readerErrorOperatorKey is the operator email used for the test runs.
	readerErrorOperatorKey = "reader-errors@example.test"
)

type readerErrorCommand struct {
	args       []string
	rows       int
	storesFail bool
}

type readerErrorDSN struct {
	name      string
	dsn       string
	required  string
	forbidden []string
}

var readerMalformedCase = readerErrorDSN{
	name: "malformed", dsn: readerMalformedDSN, required: readerFixedMessage, forbidden: []string{readerErrorSecret, readerParseErrorText},
}

// TestAuditExportReaderErrorOmitsConnectionString checks that audit.NewReader
// returns exactly readerParseFailure for a malformed DSN. It then runs audit
// export through the built server with the malformed AUDIT_READER_DSN and
// checks standard output, standard error, the text log, and stored audit rows
// for forbidden text. It also requires the log and stored failure to contain
// readerFixedMessage.
func TestAuditExportReaderErrorOmitsConnectionString(t *testing.T) {
	if _, err := audit.NewReader(t.Context(), readerMalformedDSN); err == nil || err.Error() != readerParseFailure {
		t.Fatalf("audit.NewReader with the malformed DSN returned %v, want %q", err, readerParseFailure)
	}
	ledger := startNodeAuditLedger(t)
	operatorID, now := uuid.Must(uuid.NewV7()), clock.Now().UTC()
	run := builtAuditCommand(t, readerMalformedDSN,
		"--execute", "--operator-id", operatorID.String(), "--operator-email", readerErrorOperatorKey, "--output", "json",
		"audit", "export", "--org", uuid.Must(uuid.NewV7()).String(), "--oldest", now.Add(-time.Hour).Format(time.RFC3339),
		"--latest", now.Add(time.Hour).Format(time.RFC3339), "--out", filepath.Join(t.TempDir(), "bundle"))
	if run.err == nil || !strings.Contains(run.log, readerFixedMessage) {
		t.Fatalf("audit export error = %v, want a failure and a log with %q\n%s", run.err, readerFixedMessage, run.output())
	}
	for _, forbidden := range readerMalformedCase.forbidden {
		if strings.Contains(run.output(), forbidden) {
			t.Fatalf("audit export exposed %q in output or logs:\n%s", forbidden, run.output())
		}
	}
	command := readerErrorCommand{args: []string{"audit", "export"}, rows: 2, storesFail: true}
	requireReaderErrorRows(t, ledger, readerMalformedCase, operatorID, command, run.output())
}

// TestAuditReaderErrorsOmitConnectionStrings runs ops audit consumer-offsets,
// ops audit dlq inspect, and ops audit dlq replay through the rendered ops tree
// with a malformed DSN, a slash in the password, a refused port, and a refused
// login. It checks returned errors, standard output, standard error, logs, and
// stored audit rows for forbidden text.
func TestAuditReaderErrorsOmitConnectionStrings(t *testing.T) {
	ledger := startNodeAuditLedger(t)
	parsed, err := url.Parse(testenv.Ledger(t))
	if err != nil {
		t.Fatalf("parse the ledger DSN: %v", err)
	}
	parsed.User = url.UserPassword("nobody", readerConnectSecret)
	dsns := []readerErrorDSN{
		readerMalformedCase,
		{name: "slash in the password", dsn: readerSlashPasswordDSN, required: readerConnectFailure, forbidden: []string{readerConnectSecret}},
		{name: "refused port", dsn: readerRefusedPortDSN, required: readerConnectFailure, forbidden: []string{readerConnectSecret}},
		{name: "refused login", dsn: parsed.String(), required: readerConnectFailure, forbidden: []string{readerConnectSecret}},
	}
	commands := []readerErrorCommand{
		{args: []string{"ops", "audit", "consumer-offsets"}, rows: 1, storesFail: true},
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

// runReaderErrorCommand runs one command through the rendered ops tree, checks
// its error, output, and logs, and returns their combined text.
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
