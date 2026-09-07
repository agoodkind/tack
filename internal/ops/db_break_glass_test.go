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

// runBreakGlass runs one statement through the command and returns the
// decoded report, resetting the sink between calls.
func runBreakGlass(t *testing.T, deps dbSQLDeps, statement string) (dbSQLResult, error) {
	t.Helper()
	var sink bytes.Buffer
	err := runDBSQL(context.Background(), deps, dbSQLInput{Statement: statement, Reason: "TACK-327 proof"}, &bufferSink{buf: &sink}, true)
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

// TestDBBreakGlassRunsTheStatementAndRecordsIt is the engine-backed half: a
// statement that runs returns its cells positionally with a SQL null kept as
// null, the alarm address is mailed first, and the ledger holds a pending row
// written before the statement and an ok row after, paired by attempt, each
// naming the operator, the reason, and the statement.
func TestDBBreakGlassRunsTheStatementAndRecordsIt(t *testing.T) {
	dsn := os.Getenv(appRoleTestDSNEnv)
	if dsn == "" {
		t.Skipf("set %s to a migrated ledger DSN to run", appRoleTestDSNEnv)
	}
	captured := captureBackupAlarmSends(t, nil)
	outbox := &capturedOutbox{}
	deps := breakGlassDeps(dsn, "alarm@example.test", outbox)

	result, err := runBreakGlass(t, deps, "select 42 as answer, 'glass' as answer, null as gone")
	if err != nil {
		t.Fatalf("runDBSQL: %v", err)
	}
	if result.RowsReturned != 1 || len(result.Columns) != 3 || len(result.Rows[0]) != 3 {
		t.Fatalf("result = %+v, want one row of three cells", result)
	}
	if *result.Rows[0][0] != "42" || *result.Rows[0][1] != "glass" || result.Rows[0][2] != nil {
		t.Fatalf("cells = %v, want both same-named columns kept in order and the null kept null", result.Rows[0])
	}
	if len(captured.messages) != 1 || captured.messages[0].To != "alarm@example.test" {
		t.Fatalf("mails = %+v, want one to the alarm address", captured.messages)
	}
	if len(outbox.events) != 2 {
		t.Fatalf("ledger rows = %d, want the pending row and the ok row", len(outbox.events))
	}
	pending, done := outbox.events[0], outbox.events[1]
	if pending.Outcome != audit.OutcomePending || done.Outcome != audit.OutcomeOK {
		t.Fatalf("outcomes = %s then %s, want pending then ok", pending.Outcome, done.Outcome)
	}
	pendingExtra, doneExtra := breakGlassExtra(t, pending), breakGlassExtra(t, done)
	if pendingExtra.AttemptID != doneExtra.AttemptID || pendingExtra.AttemptID == uuid.Nil {
		t.Fatal("the pending and ok rows must share one attempt id")
	}
	if done.Verb != string(audit.VerbOpsDBBreakGlass) || done.Actor.Email != "operator@example.test" {
		t.Fatalf("row = %+v, want the break-glass verb by the operator", done)
	}
	if doneExtra.Statement != "select 42 as answer, 'glass' as answer, null as gone" || doneExtra.Reason != "TACK-327 proof" || doneExtra.RowsReturned != 1 {
		t.Fatalf("extra = %+v, want the statement, the reason, and the row count", doneExtra)
	}
	if pendingExtra.Statement != doneExtra.Statement || pending.Context.Reason != "TACK-327 proof" {
		t.Fatalf("the pending row must carry the same statement and reason: %+v", pendingExtra)
	}

	// The one-statement contract is the server's: a batch is refused, and the
	// refusal is recorded as the attempt's error outcome.
	_, err = runBreakGlass(t, deps, "select 1; select 2")
	if err == nil || !strings.Contains(err.Error(), "multiple commands") {
		t.Fatalf("err = %v, want the batch refused by the server", err)
	}
	if len(outbox.events) != 4 || outbox.events[3].Outcome != audit.OutcomeError || outbox.events[3].Error == nil {
		t.Fatalf("ledger rows = %+v, want a pending row and an error row for the refused batch", outbox.events)
	}

	// A large result is cut at the row limit and says so.
	result, err = runBreakGlass(t, deps, "select generate_series(1, 2000)")
	if err != nil {
		t.Fatalf("runDBSQL: %v", err)
	}
	if result.RowsReturned != dbBreakGlassRowLimit || !result.Truncated {
		t.Fatalf("rows = %d truncated = %v, want %d rows marked truncated", result.RowsReturned, result.Truncated, dbBreakGlassRowLimit)
	}
	if extra := breakGlassExtra(t, outbox.events[5]); !extra.Truncated || extra.RowsReturned != dbBreakGlassRowLimit {
		t.Fatalf("the ok row must say the result was cut: %+v", extra)
	}
}
