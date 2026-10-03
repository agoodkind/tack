package ops

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/testenv"
	"goodkind.io/tack/internal/testenv/opsoutbox"
)

const (
	// planRecipient is the alarm address of the plan tests.
	planRecipient = "plan-alarm@example.test"
	// planService and planAccountable identify the test agent and the test
	// accountable operator, never a person. The accountable operator ID is
	// testOperatorID.
	planService     = "claude-plan-test"
	planAccountable = "accountable@example.test"
)

// planAgentFlags returns the operator flags of the test agent in session for
// the test accountable operator.
func planAgentFlags(session string) []string {
	return planAgentFlagsFor(planService, session, testOperatorID, planAccountable)
}

// planAgentFlagsFor returns the operator flags of the agent service in
// session for the accountable operator with operatorID and email.
func planAgentFlagsFor(service, session, operatorID, email string) []string {
	return []string{
		"--operator-service", service, "--operator-session", session,
		"--operator-id", operatorID, "--operator-email", email,
	}
}

// planDeps wires the plan commands to the real SQL outbox over pool, the
// msmtp account file msmtprc, and the identity source the command line
// resolves from operatorFlags.
func planDeps(t *testing.T, pool *pgxpool.Pool, dsn, msmtprc string, operatorFlags []string) dbSQLDeps {
	t.Helper()
	return dbSQLDeps{
		cfg:      &config.Config{DatabaseURL: dsn, BackupAlarmEmail: planRecipient, BackupAlarmMsmtprcPath: msmtprc},
		outbox:   audit.NewPoolOutbox(pool),
		identity: flagOperatorSource(t, operatorFlags),
	}
}

// writePlanFile writes lines as a plan file and returns its path.
func writePlanFile(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plan.sql")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write the plan file: %v", err)
	}
	return path
}

// openPlan runs plan open with --execute and returns the decoded report.
func openPlan(t *testing.T, deps dbSQLDeps, path, reason, expiresAfter string) (dbPlanOpenResult, error) {
	t.Helper()
	var sink bytes.Buffer
	input := dbPlanOpenInput{Plan: path, Reason: reason, ExpiresAfter: expiresAfter}
	err := runDBPlanOpen(t.Context(), deps, input, &bufferSink{buf: &sink}, true)
	var result dbPlanOpenResult
	decodePlanReport(t, err, &sink, &result)
	return result, err
}

// runPlanned runs one statement with --plan-id and --execute.
func runPlanned(t *testing.T, deps dbSQLDeps, planID, statement, reason string) (dbSQLResult, error) {
	t.Helper()
	var sink bytes.Buffer
	input := dbSQLInput{Statement: statement, Reason: reason, PlanID: planID}
	err := runDBSQL(t.Context(), deps, input, &bufferSink{buf: &sink}, true)
	var result dbSQLResult
	decodePlanReport(t, err, &sink, &result)
	return result, err
}

// closePlan runs plan close with --execute and returns the decoded report.
func closePlan(t *testing.T, deps dbSQLDeps, planID, postcheck string) (dbPlanCloseResult, error) {
	t.Helper()
	var sink bytes.Buffer
	input := dbPlanCloseInput{PlanID: planID, Postcheck: postcheck}
	err := runDBPlanClose(t.Context(), deps, input, &bufferSink{buf: &sink}, true)
	var result dbPlanCloseResult
	decodePlanReport(t, err, &sink, &result)
	return result, err
}

// decodePlanReport decodes the report in sink when the command succeeded.
func decodePlanReport[R dbPlanOpenResult | dbSQLResult | dbPlanCloseResult](t *testing.T, err error, sink *bytes.Buffer, result *R) {
	t.Helper()
	if err != nil {
		return
	}
	if decodeErr := json.Unmarshal(sink.Bytes(), result); decodeErr != nil {
		t.Fatalf("decode the report: %v\n%s", decodeErr, sink.String())
	}
}

// planRowsFilter selects the public.ops_outbox rows of planID.
func planRowsFilter(planID string) opsoutbox.Filter {
	return opsoutbox.Filter{Path: []string{"extra", "plan_id"}, Value: planID}
}

// planRows reads the rows of planID from public.ops_outbox, oldest first,
// and deletes them after the test.
func planRows(t *testing.T, pool *pgxpool.Pool, planID string) []audit.Event {
	t.Helper()
	filter := planRowsFilter(planID)
	deleteOutboxRowsAfterTest(t, pool, filter)
	return opsoutbox.Events(t, pool, filter)
}

// planRowKinds counts rows by verb and outcome, such as
// "ops.db_break_glass ok".
func planRowKinds(rows []audit.Event) map[string]int {
	kinds := map[string]int{}
	for _, row := range rows {
		kinds[row.Verb+" "+string(row.Outcome)]++
	}
	return kinds
}

// mailWithSubject returns the one delivered message with a subject that
// contains fragment, and fails the test when the count differs from one.
func mailWithSubject(t *testing.T, messages []testenv.MailpitMessage, fragment string) testenv.MailpitMessage {
	t.Helper()
	matched := mailsWithSubject(messages, fragment)
	if len(matched) != 1 {
		t.Fatalf("mails with a subject containing %q = %d, want 1; all mails: %+v", fragment, len(matched), messages)
	}
	return matched[0]
}

// mailsWithSubject returns the messages with a subject that contains
// fragment.
func mailsWithSubject(messages []testenv.MailpitMessage, fragment string) []testenv.MailpitMessage {
	var matched []testenv.MailpitMessage
	for _, message := range messages {
		if strings.Contains(message.Subject, fragment) {
			matched = append(matched, message)
		}
	}
	return matched
}

// requireMailCount reads the mailbox and fails the test unless it contains
// count messages.
func requireMailCount(t *testing.T, mail testenv.MailpitFixture, count int) []testenv.MailpitMessage {
	t.Helper()
	messages := deliveredMail(t, mail)
	if len(messages) != count {
		t.Fatalf("mails = %+v, want %d", messages, count)
	}
	return messages
}

// deliveredMail reads every message in the Mailpit mailbox.
func deliveredMail(t *testing.T, mail testenv.MailpitFixture) []testenv.MailpitMessage {
	t.Helper()
	messages, err := mail.Messages(t.Context())
	if err != nil {
		t.Fatalf("read the Mailpit mailbox: %v", err)
	}
	return messages
}
