package integration

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/testenv"
)

const (
	// attributionService and attributionSession identify the test agent.
	attributionService = "claude-test-agent"
	attributionSession = "session-test-1"
	// attributionEmail is the test accountable operator, never a person.
	attributionEmail     = "accountable@example.test"
	attributionReason    = "attribution test"
	attributionRecipient = "break-glass-alarm@example.test"
	// actorKindService is the audit.events actor_kind code of a service.
	actorKindService = 2
)

// TestOpsDBSQLRecordsAgentAttribution runs `ops db sql --execute` through the
// audited command tree as a service agent acting for an accountable operator,
// against the real ledger, the real SQL outbox, and the real SMTP server. The
// alarm mail states the agent, its session, and the accountable operator.
// The choke-point intent and outcome rows and the break-glass pending and ok
// rows record the agent as the actor and the operator in on_behalf_of. The
// real relay and consumer then project the same four events into audit.events
// with actor kind 2 and the same extra payload. A signed export of those rows
// passes audit.VerifyBundle.
func TestOpsDBSQLRecordsAgentAttribution(t *testing.T) {
	ledgerDSN := testenv.Ledger(t)
	mail := testenv.Mailpit(t)
	brokers := testenv.Kafka(t)
	pool := openAttributionPool(t, ledgerDSN)
	operatorID := accountableOperatorID(t, attributionEmail)
	if err := mail.DeleteAll(t.Context()); err != nil {
		t.Fatalf("clear the Mailpit mailbox: %v", err)
	}
	since := ledgerNow(t, pool)
	output, err := runOpsDBSQL(t, breakGlassConfig(ledgerDSN, mail), pool,
		"--execute", "--operator-service", attributionService, "--operator-session", attributionSession,
		"--operator-id", operatorID.String(), "--operator-email", attributionEmail,
		"ops", "db", "sql", "--statement", "select 1", "--reason", attributionReason)
	if err != nil {
		t.Fatalf("ops db sql --execute: %v\n%s", err, output)
	}

	messages, err := mail.Messages(t.Context())
	if err != nil {
		t.Fatalf("read the Mailpit mailbox: %v", err)
	}
	agentLine := "Agent: " + attributionService + " (session " + attributionSession + ") for " + attributionEmail
	if len(messages) != 1 || !strings.HasSuffix(messages[0].Subject, "by "+attributionEmail) ||
		!strings.Contains(messages[0].Text, agentLine) {
		t.Fatalf("delivered mail = %+v, want one message with a subject ending in %q and the text line %q",
			messages, "by "+attributionEmail, agentLine)
	}

	events := outboxEventsSince(t, pool, since)
	eventIDs := requireOutboxAttribution(t, events, operatorID)
	startAuditPipeline(t, brokers, ledgerDSN, pool)
	for _, row := range waitForLedgerEvents(t, pool, eventIDs) {
		if row.ActorKind != actorKindService || row.ActorID != cli.ServiceActorID(attributionService) {
			t.Fatalf("audit.events row %s actor = kind %d id %s, want kind %d id %s",
				row.EventID, row.ActorKind, row.ActorID, actorKindService, cli.ServiceActorID(attributionService))
		}
		extra := decodeAttributionExtra(t, []byte(row.Extra))
		requireAccountable(t, "audit.events row "+row.EventID, extra, operatorID, extra.AttemptID != uuid.Nil)
	}
	requireVerifiedBundle(t, ledgerDSN, pool, since, len(eventIDs))
}

// TestOpsDBSQLRefusesSessionWithoutService requires `ops db sql --execute`
// with a session and an operator but no service to refuse at the identity
// check, before the command mails or writes an outbox row.
func TestOpsDBSQLRefusesSessionWithoutService(t *testing.T) {
	ledgerDSN := testenv.Ledger(t)
	mail := testenv.Mailpit(t)
	pool := openAttributionPool(t, ledgerDSN)
	operatorID := accountableOperatorID(t, attributionEmail)
	if err := mail.DeleteAll(t.Context()); err != nil {
		t.Fatalf("clear the Mailpit mailbox: %v", err)
	}
	since := ledgerNow(t, pool)
	_, err := runOpsDBSQL(t, breakGlassConfig(ledgerDSN, mail), pool,
		"--execute", "--operator-session", attributionSession,
		"--operator-id", operatorID.String(), "--operator-email", attributionEmail,
		"ops", "db", "sql", "--statement", "select 1", "--reason", attributionReason)
	if err == nil || !strings.Contains(err.Error(), "--operator-session requires --operator-service") {
		t.Fatalf("ops db sql with a session and no service = %v, want the session refusal", err)
	}
	messages, err := mail.Messages(t.Context())
	if err != nil {
		t.Fatalf("read the Mailpit mailbox: %v", err)
	}
	if len(messages) != 0 {
		t.Fatalf("delivered mail = %+v, want none for a refused identity", messages)
	}
	if events := outboxEventsSince(t, pool, since); len(events) != 0 {
		t.Fatalf("operator outbox rows = %+v, want none for a refused identity", events)
	}
}

// breakGlassConfig points the statement at the test ledger and the alarm
// mail at the Mailpit account.
func breakGlassConfig(ledgerDSN string, mail testenv.MailpitFixture) *config.Config {
	return &config.Config{
		DatabaseURL: ledgerDSN, BackupAlarmEmail: attributionRecipient, BackupAlarmMsmtprcPath: mail.Msmtprc,
	}
}

// requireOutboxAttribution requires exactly the choke-point intent and
// outcome rows and the break-glass pending and ok rows, each recording the
// agent as the actor and the accountable operator, and returns their IDs.
func requireOutboxAttribution(t *testing.T, events []audit.Event, operatorID uuid.UUID) []string {
	t.Helper()
	kinds := map[string]int{}
	eventIDs := make([]string, 0, len(events))
	for _, event := range events {
		extra := decodeAttributionExtra(t, event.Extra)
		breakGlass := extra.AttemptID != uuid.Nil
		kind := "choke-point " + string(event.Outcome)
		if breakGlass {
			kind = "break-glass " + string(event.Outcome)
		}
		kinds[kind]++
		label := "outbox " + kind + " row " + event.EventID.String()
		actor := event.Actor
		if event.Verb != string(audit.VerbOpsDBBreakGlass) || actor.Type != audit.ActorService ||
			actor.ID != cli.ServiceActorID(attributionService) || actor.Name != attributionService ||
			actor.SessionID != attributionSession {
			t.Fatalf("%s = verb %s actor %+v, want %s by service %s in session %s",
				label, event.Verb, actor, audit.VerbOpsDBBreakGlass, attributionService, attributionSession)
		}
		requireAccountable(t, label, extra, operatorID, breakGlass)
		eventIDs = append(eventIDs, event.EventID.String())
	}
	want := map[string]int{"choke-point pending": 1, "choke-point ok": 1, "break-glass pending": 1, "break-glass ok": 1}
	if len(events) != len(want) || len(kinds) != len(want) {
		t.Fatalf("outbox row kinds = %v, want one each of %v", kinds, want)
	}
	for kind, count := range want {
		if kinds[kind] != count {
			t.Fatalf("outbox row kinds = %v, want one each of %v", kinds, want)
		}
	}
	return eventIDs
}

// requireAccountable requires the session and the accountable operator in
// extra, and on a break-glass row the command's reason.
func requireAccountable(t *testing.T, label string, extra attributionExtra, operatorID uuid.UUID, breakGlass bool) {
	t.Helper()
	accountable := extra.OnBehalfOf
	if extra.SessionID != attributionSession || accountable == nil ||
		accountable.OperatorID != operatorID || accountable.OperatorEmail != attributionEmail {
		t.Fatalf("%s extra = %+v on behalf of %+v, want session %s for %s (%s)",
			label, extra, accountable, attributionSession, attributionEmail, operatorID)
	}
	if breakGlass && accountable.Reason != attributionReason {
		t.Fatalf("%s on_behalf_of.reason = %q, want %q", label, accountable.Reason, attributionReason)
	}
}
