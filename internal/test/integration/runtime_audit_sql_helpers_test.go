package integration_test

import (
	"context"
	_ "embed"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/testenv"
)

//go:embed testdata/runtime_audit_outbox_events.sql
var runtimeAuditOutboxEventsQuery string

const (
	sqlAuditRelayInterval = 100 * time.Millisecond
	sqlAuditRelayBatch    = 64
	sqlAuditProduceWait   = 30 * time.Second
	sqlAuditTool          = "runtime-audit-sql-test"
)

func sqlAuditPrincipal() audit.OperatorPrincipal {
	return audit.OperatorPrincipal{
		ID: uuid.Must(uuid.NewV7()), Email: "operator@example.invalid", Name: "Operator", Source: "test",
	}
}

// sqlAuditContext creates a mutation operation for principal and attaches it
// to t.Context(). It returns the resulting context and operation.
func sqlAuditContext(t *testing.T, principal audit.OperatorPrincipal) (context.Context, audit.Event) {
	t.Helper()
	operation, err := audit.NewMutationOperation(audit.Actor{
		Type: principal.ActorType(), ID: principal.ID, Email: principal.Email, Name: principal.Name,
		SessionID: "", IP: "", UserAgent: "", RequestID: "", APITokenLabel: "",
	}, audit.SourceSystem, sqlAuditTool)
	if err != nil {
		t.Fatalf("audit.NewMutationOperation error = %v, want nil", err)
	}
	attached, err := audit.WithMutationContext(t.Context(), operation)
	if err != nil {
		t.Fatalf("audit.WithMutationContext error = %v, want nil", err)
	}
	return attached, operation
}

// sqlOperatorContext attaches principal and a new UUIDv7 command operation ID
// to ctx as the clispec command gate does. It returns the resulting context
// and command operation ID.
func sqlOperatorContext(ctx context.Context, principal audit.OperatorPrincipal) (context.Context, uuid.UUID) {
	commandID := uuid.Must(uuid.NewV7())
	return audit.WithOperatorPrincipal(audit.WithOpID(ctx, commandID), principal), commandID
}

// sqlOutboxEvents reads operator outbox events matching id as an entity ID or
// operation ID, in creation order.
func sqlOutboxEvents(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) []audit.Event {
	t.Helper()
	rows, err := pool.Query(t.Context(), runtimeAuditOutboxEventsQuery, id.String())
	if err != nil {
		t.Fatalf("query outbox events for %s: got error %v, want nil", id, err)
	}
	defer rows.Close()
	events := make([]audit.Event, 0)
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatalf("scan outbox event: got error %v, want nil", err)
		}
		var event audit.Event
		if err := json.Unmarshal(raw, &event); err != nil {
			t.Fatalf("decode outbox event: got error %v, want nil", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate outbox events for %s: got error %v, want nil", id, err)
	}
	return events
}

// startOutboxRelay runs the real relay to publish operator outbox events to the
// test Kafka topic until the test ends.
func startOutboxRelay(t *testing.T, stack runtimeAuditStack) {
	t.Helper()
	recorder, err := audit.NewKafkaRecorder(audit.KafkaConfig{
		Brokers: []string{testenv.Kafka(t)}, Topic: stack.topic, ClientID: "tack-runtime-audit-relay",
		ProduceTimeout: sqlAuditProduceWait,
	})
	if err != nil {
		t.Fatalf("NewKafkaRecorder error = %v, want nil", err)
	}
	relay, err := audit.NewRelay(audit.RelayConfig{
		Recorder: recorder, Yugabyte: audit.NewPoolOutbox(stack.admin), FoundationDB: nil,
		PollInterval: sqlAuditRelayInterval, BatchSize: sqlAuditRelayBatch,
	})
	if err != nil {
		t.Fatalf("NewRelay error = %v, want nil", err)
	}
	relay.Start(t.Context())
	t.Cleanup(func() { _ = relay.Close() })
}

// requireLedgerChange waits for the ledger row for sent and requires
// audit.OperationDatabaseChange. It compares the target, outcome, phase,
// operation ID, actor ID, action, and error with sent.
func requireLedgerChange(t *testing.T, stack runtimeAuditStack, sent audit.Event) audit.Row {
	t.Helper()
	row := stack.waitForRow(t, sent.EventID)
	if !reflect.DeepEqual(row.Context.Target, sent.Context.Target) {
		t.Fatalf("event %s target = %+v, want %+v", sent.EventID, row.Context.Target, sent.Context.Target)
	}
	if row.Outcome != sent.Outcome || row.Context.Phase != sent.Context.Phase ||
		row.Context.OperationClass != audit.OperationDatabaseChange {
		t.Fatalf("event %s Outcome, Phase, OperationClass = %s, %s, %s; want sent.Outcome, sent.Context.Phase, audit.OperationDatabaseChange", sent.EventID, row.Outcome, row.Context.Phase, row.Context.OperationClass)
	}
	if row.Context.OperationID != sent.Context.OperationID || row.ActorID != sent.Actor.ID || row.Action != sent.Verb {
		t.Fatalf("event %s OperationID = %s, ActorID = %s, Action = %s; want sent.Context.OperationID, sent.Actor.ID, sent.Verb", sent.EventID, row.Context.OperationID, row.ActorID, row.Action)
	}
	if !reflect.DeepEqual(row.Error, sent.Error) {
		t.Fatalf("event %s error = %+v, want %+v", sent.EventID, row.Error, sent.Error)
	}
	return row
}

func onlyEvent(t *testing.T, events []audit.Event, phase audit.Phase) audit.Event {
	t.Helper()
	if len(events) != 1 || events[0].Context.Phase != phase {
		t.Fatalf("outbox events = %+v, want one %s event", events, phase)
	}
	return events[0]
}
