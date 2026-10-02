package ops

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/testenv"
)

// TestDBBreakGlassRefusesToRunUnobserved pins the control: no reason, no
// recipient, or a mail the SMTP server cannot accept each stop the statement
// before the command dials the database. The database address here refuses
// connections. A statement that ran would fail on that dial and name the
// address; none of these errors name it. The mail account points at a closed
// local port, and the real SQL outbox records no row for the statement that
// never ran.
func TestDBBreakGlassRefusesToRunUnobserved(t *testing.T) {
	pool := testenv.LedgerPool(t, testenv.Ledger(t))
	unreachable := unreachableMsmtprc(t)
	reason := "incident 42 " + uuid.NewString()[:8]
	var sink bytes.Buffer
	deps := breakGlassDeps(t, pool, "postgres://tack@[::1]:2/tack", "alarm@example.test", unreachable)

	err := runDBSQL(t.Context(), deps, dbSQLInput{Statement: "select 1", Reason: "  "}, &bufferSink{buf: &sink}, true)
	if err == nil || !strings.Contains(err.Error(), "reason is required") {
		t.Fatalf("err = %v, want the missing reason refused", err)
	}

	noRecipient := breakGlassDeps(t, pool, "postgres://tack@[::1]:2/tack", "", unreachable)
	err = runDBSQL(t.Context(), noRecipient, dbSQLInput{Statement: "select 1", Reason: reason}, &bufferSink{buf: &sink}, true)
	if err == nil || !strings.Contains(err.Error(), "TACK_BACKUP_ALARM_EMAIL is empty") {
		t.Fatalf("err = %v, want the missing recipient refused", err)
	}

	err = runDBSQL(t.Context(), deps, dbSQLInput{Statement: "select 1", Reason: reason}, &bufferSink{buf: &sink}, true)
	if err == nil || !strings.Contains(err.Error(), "was not delivered") || !strings.Contains(err.Error(), "the statement did not run") {
		t.Fatalf("err = %v, want the undelivered mail to stop the statement", err)
	}
	if !strings.Contains(err.Error(), "smtp dial localhost:1") {
		t.Fatalf("err = %v, want the attempted SMTP dial to the closed port named", err)
	}
	if strings.Contains(err.Error(), "[::1]:2") {
		t.Fatalf("the database was dialed before the mail was confirmed: %v", err)
	}
	if rows := breakGlassRows(t, pool, reason); len(rows) != 0 {
		t.Fatalf("ledger rows = %+v, want no row for a statement that never ran", rows)
	}
}

// TestDBBreakGlassRunsTheStatementAndRecordsIt is the engine-backed half: a
// statement that runs returns its cells positionally with a SQL null kept as
// null, and the alarm address receives the mail first. The ledger outbox
// contains a pending row written before the statement and an ok row after,
// paired by attempt, each naming the operator, the reason, and the statement.
func TestDBBreakGlassRunsTheStatementAndRecordsIt(t *testing.T) {
	ledgerDSN := testenv.Ledger(t)
	mail := mailpitFor(t)
	pool := testenv.LedgerPool(t, ledgerDSN)
	reason := "TACK-327 proof " + uuid.NewString()[:8]
	statement := "select 42 as answer, 'glass' as answer, null as gone"
	deps := breakGlassDeps(t, pool, ledgerDSN, "alarm@example.test", mail.Msmtprc)

	result, err := runBreakGlass(t, deps, statement, reason)
	if err != nil {
		t.Fatalf("runDBSQL: %v", err)
	}
	if result.RowsReturned != 1 || len(result.Columns) != 3 || len(result.Rows[0]) != 3 {
		t.Fatalf("result = %+v, want one row of three cells", result)
	}
	if *result.Rows[0][0] != "42" || *result.Rows[0][1] != "glass" || result.Rows[0][2] != nil {
		t.Fatalf("cells = %v, want both same-named columns kept in order and the null kept null", result.Rows[0])
	}
	messages, err := mail.Messages(t.Context())
	if err != nil {
		t.Fatalf("read the Mailpit mailbox: %v", err)
	}
	if len(messages) != 1 || !slices.Equal(messages[0].To, []string{"alarm@example.test"}) {
		t.Fatalf("mails = %+v, want one to the alarm address", messages)
	}
	if !strings.Contains(messages[0].Text, reason) || !strings.Contains(messages[0].Text, statement) {
		t.Fatalf("the mail must carry the reason and the statement, got %q", messages[0].Text)
	}
	rows := breakGlassRows(t, pool, reason)
	if len(rows) != 2 {
		t.Fatalf("ledger rows = %d, want the pending row and the ok row", len(rows))
	}
	pending, done := rows[0], rows[1]
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
	if doneExtra.Statement != statement || doneExtra.Reason != reason || doneExtra.RowsReturned != 1 {
		t.Fatalf("extra = %+v, want the statement, the reason, and the row count", doneExtra)
	}
	if pendingExtra.Statement != doneExtra.Statement || pending.Context.Reason != reason {
		t.Fatalf("the pending row must carry the same statement and reason: %+v", pendingExtra)
	}

	// The one-statement contract is the server's: a batch is refused, and the
	// refusal is recorded as the attempt's error outcome.
	_, err = runBreakGlass(t, deps, "select 1; select 2", reason)
	if err == nil || !strings.Contains(err.Error(), "multiple commands") {
		t.Fatalf("err = %v, want the batch refused by the server", err)
	}
	rows = breakGlassRows(t, pool, reason)
	if len(rows) != 4 || rows[3].Outcome != audit.OutcomeError || rows[3].Error == nil {
		t.Fatalf("ledger rows = %+v, want a pending row and an error row for the refused batch", rows)
	}

	// A large result is cut at the row limit and says so.
	result, err = runBreakGlass(t, deps, "select generate_series(1, 2000)", reason)
	if err != nil {
		t.Fatalf("runDBSQL: %v", err)
	}
	if result.RowsReturned != dbBreakGlassRowLimit || !result.Truncated {
		t.Fatalf("rows = %d truncated = %v, want %d rows marked truncated", result.RowsReturned, result.Truncated, dbBreakGlassRowLimit)
	}
	rows = breakGlassRows(t, pool, reason)
	if len(rows) != 6 {
		t.Fatalf("ledger rows = %d, want three pending and outcome pairs", len(rows))
	}
	if extra := breakGlassExtra(t, rows[5]); !extra.Truncated || extra.RowsReturned != dbBreakGlassRowLimit {
		t.Fatalf("the ok row must say the result was cut: %+v", extra)
	}
}
