package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/testenv"
)

// breakGlassMailTests lists the tests in this package that deliver to the
// Mailpit server.
var breakGlassMailTests = []string{"TestDBBreakGlassRunsTheStatementAndRecordsIt"}

// unreachableMsmtprcFormat is an msmtp account on the closed local port 1.
// The production mailer fails at the SMTP dial to it.
const unreachableMsmtprcFormat = `account unreachable
host localhost
port 1
from tack-test@example.test
auth on
user tack-test
password unreachable-placeholder
tls on
tls_starttls on

account default : unreachable
`

// bufferSink collects what a command reports, JSON and text alike.
type bufferSink struct {
	buf *bytes.Buffer
}

func (s *bufferSink) WriteJSON(_ context.Context, payload json.RawMessage) error {
	_, err := s.buf.Write(payload)
	return err
}

func (s *bufferSink) WriteText(_ context.Context, body string) error {
	_, err := s.buf.WriteString(body)
	return err
}

// breakGlassLedgerPool opens a superuser pool on the test ledger.
func breakGlassLedgerPool(t *testing.T, ledgerDSN string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), ledgerDSN)
	if err != nil {
		t.Fatalf("open the ledger pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// unreachableMsmtprc writes the closed-port account file and returns its path.
func unreachableMsmtprc(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "msmtprc")
	if err := os.WriteFile(path, []byte(unreachableMsmtprcFormat), 0o600); err != nil { // gitleaks:allow test placeholder
		t.Fatalf("write the unreachable msmtp account: %v", err)
	}
	return path
}

// breakGlassDeps wires the command to the real SQL outbox over pool and to
// the identity source the command line resolves from the operator flags.
func breakGlassDeps(t *testing.T, pool *pgxpool.Pool, dsn, recipient, msmtprc string) dbSQLDeps {
	t.Helper()
	factory := &cli.Factory{Cfg: nil, In: nil, Out: nil, Err: nil}
	root := &cobra.Command{Use: "tack"}
	factory.RegisterGlobalFlags(root)
	if err := root.ParseFlags([]string{
		"--operator-id", "019ff315-bc5d-7a56-b12a-1a35f280c4dd",
		"--operator-email", "operator@example.test", "--operator-name", "Operator",
	}); err != nil {
		t.Fatalf("parse the operator flags: %v", err)
	}
	return dbSQLDeps{
		cfg:      &config.Config{DatabaseURL: dsn, BackupAlarmEmail: recipient, BackupAlarmMsmtprcPath: msmtprc},
		outbox:   audit.NewPoolOutbox(pool),
		identity: cli.NewOperatorSource(factory),
	}
}

// breakGlassRows reads the break-glass rows recorded with reason from
// public.ops_outbox, oldest first, and deletes them after the test.
func breakGlassRows(t *testing.T, pool *pgxpool.Pool, reason string) []audit.Event {
	t.Helper()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(t.Context()),
			`DELETE FROM public.ops_outbox WHERE event->'context'->>'reason' = $1`, reason)
	})
	rows, err := pool.Query(t.Context(), `
		SELECT event FROM public.ops_outbox
		 WHERE event->>'verb' = $1 AND event->'context'->>'reason' = $2
		 ORDER BY created_at, event_id`, string(audit.VerbOpsDBBreakGlass), reason)
	if err != nil {
		t.Fatalf("read the operator outbox: %v", err)
	}
	encoded, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
	if err != nil {
		t.Fatalf("read the operator outbox rows: %v", err)
	}
	events := make([]audit.Event, 0, len(encoded))
	for _, body := range encoded {
		var event audit.Event
		if err := json.Unmarshal(body, &event); err != nil {
			t.Fatalf("decode the operator outbox event %s: %v", body, err)
		}
		events = append(events, event)
	}
	return events
}

// runBreakGlass runs one statement through the command and returns the
// decoded report, resetting the sink between calls.
func runBreakGlass(t *testing.T, deps dbSQLDeps, statement, reason string) (dbSQLResult, error) {
	t.Helper()
	var sink bytes.Buffer
	err := runDBSQL(t.Context(), deps, dbSQLInput{Statement: statement, Reason: reason}, &bufferSink{buf: &sink}, true)
	var result dbSQLResult
	if err == nil {
		if decodeErr := json.Unmarshal(sink.Bytes(), &result); decodeErr != nil {
			t.Fatalf("decode the report: %v\n%s", decodeErr, sink.String())
		}
	}
	return result, err
}

func breakGlassExtra(t *testing.T, row audit.Event) dbBreakGlassExtra {
	t.Helper()
	var extra dbBreakGlassExtra
	if err := json.Unmarshal(row.Extra, &extra); err != nil {
		t.Fatalf("decode the row's extra: %v", err)
	}
	return extra
}

// mailpitFor clears the shared Mailpit mailbox and returns the server.
func mailpitFor(t *testing.T) testenv.MailpitFixture {
	t.Helper()
	mail := testenv.Mailpit(t)
	if err := mail.DeleteAll(t.Context()); err != nil {
		t.Fatalf("clear the Mailpit mailbox: %v", err)
	}
	return mail
}
