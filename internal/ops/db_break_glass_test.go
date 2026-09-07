package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/config"
)

// capturedOutbox keeps every event a command records, so a test reads the
// ledger row the way an auditor would rather than trusting the report.
type capturedOutbox struct {
	events []audit.Event
}

func (o *capturedOutbox) WriteOutbox(_ context.Context, event audit.Event) error {
	o.events = append(o.events, event)
	return nil
}

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

type fixedOperator struct {
	principal audit.OperatorPrincipal
}

func (f fixedOperator) Resolve(context.Context) (audit.OperatorPrincipal, error) {
	return f.principal, nil
}

func breakGlassDeps(dsn, recipient string, outbox *capturedOutbox) dbSQLDeps {
	return dbSQLDeps{
		cfg:    &config.Config{DatabaseURL: dsn, BackupAlarmEmail: recipient},
		outbox: outbox,
		identity: fixedOperator{principal: audit.OperatorPrincipal{
			ID: uuid.MustParse("019ff315-bc5d-7a56-b12a-1a35f280c4dd"), Email: "operator@example.test",
			Name: "Operator", Source: "test",
		}},
	}
}

// TestDBBreakGlassRefusesToRunUnobserved pins the control: no reason, no
// recipient, or a mail the relay refused each stop the statement before the
// database is touched. The database here refuses connections, so a statement
// that ran would fail on the dial and name it; none of these errors do.
func TestDBBreakGlassRefusesToRunUnobserved(t *testing.T) {
	captured := captureBackupAlarmSends(t, nil)
	outbox := &capturedOutbox{}
	var sink bytes.Buffer
	deps := breakGlassDeps("postgres://tack@127.0.0.1:1/tack", "alarm@example.test", outbox)

	err := runDBSQL(context.Background(), deps, dbSQLInput{Statement: "select 1", Reason: "  "}, &bufferSink{buf: &sink}, true)
	if err == nil || !strings.Contains(err.Error(), "reason is required") {
		t.Fatalf("err = %v, want the missing reason refused", err)
	}

	noRecipient := breakGlassDeps("postgres://tack@127.0.0.1:1/tack", "", outbox)
	err = runDBSQL(context.Background(), noRecipient, dbSQLInput{Statement: "select 1", Reason: "incident 42"}, &bufferSink{buf: &sink}, true)
	if err == nil || !strings.Contains(err.Error(), "TACK_BACKUP_ALARM_EMAIL is empty") {
		t.Fatalf("err = %v, want the missing recipient refused", err)
	}

	captured.sendErr = errors.New("smtp dial mail.example.test:587: connection refused")
	err = runDBSQL(context.Background(), deps, dbSQLInput{Statement: "select 1", Reason: "incident 42"}, &bufferSink{buf: &sink}, true)
	if err == nil || !strings.Contains(err.Error(), "was not delivered, so the statement did not run") {
		t.Fatalf("err = %v, want the undelivered mail to stop the statement", err)
	}
	if strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Fatalf("the database was dialed before the mail was confirmed: %v", err)
	}
	if len(captured.messages) != 1 || len(outbox.events) != 0 {
		t.Fatalf("mails = %d, ledger rows = %d; want one attempted mail and no row for a statement that never ran",
			len(captured.messages), len(outbox.events))
	}
	if !strings.Contains(captured.messages[0].Body, "incident 42") || !strings.Contains(captured.messages[0].Body, "select 1") {
		t.Fatalf("the mail must carry the reason and the statement, got %q", captured.messages[0].Body)
	}
}

// TestDBBreakGlassRunsTheStatementAndRecordsIt is the engine-backed half: a
// statement that runs returns its rows, the alarm address is mailed first, and
// the ledger row names the operator, the reason, and the statement.
func TestDBBreakGlassRunsTheStatementAndRecordsIt(t *testing.T) {
	dsn := os.Getenv(appRoleTestDSNEnv)
	if dsn == "" {
		t.Skipf("set %s to a migrated ledger DSN to run", appRoleTestDSNEnv)
	}
	captured := captureBackupAlarmSends(t, nil)
	outbox := &capturedOutbox{}
	var sink bytes.Buffer
	deps := breakGlassDeps(dsn, "alarm@example.test", outbox)

	err := runDBSQL(context.Background(), deps, dbSQLInput{
		Statement: "select 42 as answer, 'glass' as pane", Reason: "TACK-327 proof",
	}, &bufferSink{buf: &sink}, true)
	if err != nil {
		t.Fatalf("runDBSQL: %v", err)
	}
	var result dbSQLResult
	if err := json.Unmarshal(sink.Bytes(), &result); err != nil {
		t.Fatalf("decode the report: %v\n%s", err, sink.String())
	}
	if result.RowsReturned != 1 || result.Rows[0]["answer"] != "42" || result.Rows[0]["pane"] != "glass" {
		t.Fatalf("result = %+v, want the one row the statement selects", result)
	}
	if len(captured.messages) != 1 || captured.messages[0].To != "alarm@example.test" {
		t.Fatalf("mails = %+v, want one to the alarm address", captured.messages)
	}
	if len(outbox.events) != 1 {
		t.Fatalf("ledger rows = %d, want the one detail row", len(outbox.events))
	}
	row := outbox.events[0]
	var extra dbBreakGlassExtra
	if err := json.Unmarshal(row.Extra, &extra); err != nil {
		t.Fatalf("decode the row's extra: %v", err)
	}
	if row.Verb != string(audit.VerbOpsDBBreakGlass) || row.Actor.Email != "operator@example.test" || row.Outcome != audit.OutcomeOK {
		t.Fatalf("row = %+v, want the break-glass verb by the operator with outcome ok", row)
	}
	if extra.Statement != "select 42 as answer, 'glass' as pane" || extra.Reason != "TACK-327 proof" || extra.RowsReturned != 1 {
		t.Fatalf("extra = %+v, want the statement, the reason, and the row count", extra)
	}
	if row.Context.Reason != "TACK-327 proof" {
		t.Fatalf("context reason = %q, want the reason", row.Context.Reason)
	}
}
