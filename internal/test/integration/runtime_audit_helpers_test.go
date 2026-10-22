package integration_test

import (
	"crypto/ed25519"
	"crypto/rand"
	_ "embed"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/testenv"
)

//go:embed testdata/runtime_audit_dead_letter.sql
var runtimeAuditDeadLetterQuery string

// runtimeAuditDeadline is the maximum time each test waits for the
// consumer.
const runtimeAuditDeadline = 90 * time.Second

// runtimeAuditStack contains a running audit consumer, its Kafka producer,
// ledger reader, and administrator database pool on one Kafka topic.
type runtimeAuditStack struct {
	topic    string
	admin    *pgxpool.Pool
	reader   *audit.Reader
	recorder *audit.KafkaRecorder
}

// startRuntimeAuditStack starts the real audit consumer on a new Kafka
// topic against the test ledger and broker.
func startRuntimeAuditStack(t *testing.T) runtimeAuditStack {
	t.Helper()
	ctx := t.Context()
	adminDSN := testenv.Ledger(t)
	brokers := testenv.Kafka(t)
	admin, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		t.Fatalf("admin pool: %v", err)
	}
	t.Cleanup(admin.Close)
	topic := "audit.runtime-test-" + uuid.NewString()[:8]
	consumer, err := audit.NewConsumer(ctx, audit.ConsumerConfig{
		Brokers: []string{brokers}, Topic: topic, GroupID: "tack-runtime-audit-" + uuid.NewString()[:8],
		BatchSize: 32, PollInterval: 100 * time.Millisecond, YugabyteDSN: adminDSN,
	})
	if err != nil {
		t.Fatalf("start the audit consumer: %v", err)
	}
	consumer.Start(ctx)
	t.Cleanup(func() { _ = consumer.Close() })
	reader, err := audit.NewReader(ctx, adminDSN)
	if err != nil {
		t.Fatalf("open the audit reader: %v", err)
	}
	t.Cleanup(reader.Close)
	recorder, err := audit.NewKafkaRecorder(audit.KafkaConfig{
		Brokers: []string{brokers}, Topic: topic, ClientID: "tack-runtime-audit-test", ProduceTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("open the audit producer: %v", err)
	}
	t.Cleanup(func() { _ = recorder.Close() })
	return runtimeAuditStack{topic: topic, admin: admin, reader: reader, recorder: recorder}
}

func (stack runtimeAuditStack) record(t *testing.T, events ...audit.Event) {
	t.Helper()
	for _, event := range events {
		if err := stack.recorder.Record(t.Context(), event); err != nil {
			t.Fatalf("record audit event %s: %v", event.EventID, err)
		}
	}
}

// waitForRow reads one event through the ledger reader until the consumer
// stores it.
func (stack runtimeAuditStack) waitForRow(t *testing.T, eventID uuid.UUID) audit.Row {
	t.Helper()
	deadline := clock.Now().Add(runtimeAuditDeadline)
	for {
		row, err := stack.reader.GetByID(t.Context(), eventID)
		if err == nil {
			return *row
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("read audit event %s: %v", eventID, err)
		}
		if clock.Now().After(deadline) {
			t.Fatalf("ledger reader did not return audit event %s within %s", eventID, runtimeAuditDeadline)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// waitForDeadLetters waits until the consumer stores the requested number
// of dead-letter rows for the topic. It returns their error texts.
func (stack runtimeAuditStack) waitForDeadLetters(t *testing.T, want int) []string {
	t.Helper()
	deadline := clock.Now().Add(runtimeAuditDeadline)
	for {
		rows, err := stack.admin.Query(t.Context(), runtimeAuditDeadLetterQuery, stack.topic)
		if err != nil {
			t.Fatalf("read dead letters for %s: %v", stack.topic, err)
		}
		errorTexts, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatalf("scan dead letters for %s: %v", stack.topic, err)
		}
		if len(errorTexts) == want {
			return errorTexts
		}
		if len(errorTexts) > want || clock.Now().After(deadline) {
			t.Fatalf("dead letters for %s = %d, want %d", stack.topic, len(errorTexts), want)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// exportAndVerify exports one org time window to a signed bundle and
// returns the offline verification result.
func (stack runtimeAuditStack) exportAndVerify(t *testing.T, orgID uuid.UUID, oldest, latest time.Time) error {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("signing key: %v", err)
	}
	dir := t.TempDir()
	filter := audit.QueryFilter{
		OrgID: orgID, Oldest: oldest, Latest: latest, Action: "", ActorID: uuid.Nil, EntityID: uuid.Nil,
		RequestID: "", TraceID: "", Limit: 0,
	}
	if _, err := audit.Export(t.Context(), stack.reader, private, audit.KeyIdentifier(public), filter, dir); err != nil {
		t.Fatalf("export org %s: %v", orgID, err)
	}
	report, err := audit.VerifyBundle(dir, public)
	if err != nil {
		t.Fatalf("verify the bundle for org %s: %v", orgID, err)
	}
	if report.RowsScanned == 0 {
		t.Fatalf("export for org %s did not write any rows", orgID)
	}
	return report.Err()
}

// completeEvent returns a format 2 event with the fields every class
// shares.
func completeEvent(orgID uuid.UUID, actor audit.Actor, target audit.Target, class audit.OperationClass, phase audit.Phase, outcome audit.Outcome) audit.Event {
	now := clock.Now().UTC()
	return audit.Event{
		Verb: "runtime.audit.test", EventID: uuid.Must(uuid.NewV7()), Actor: actor,
		Entity: audit.Entity{Type: target.Kind, NodeType: target.NodeType, ID: target.ID, Identifier: "", Name: ""},
		Context: audit.EventContext{
			OrgID: orgID, WorkspaceID: uuid.Nil, ScopeID: uuid.Nil, ParentID: uuid.Nil,
			RequestID: "", TraceID: "", Source: audit.SourceMCP, Tool: "", RPC: "", Reason: "",
			AuditFormatVersion: audit.AuditFormatComplete, OperationClass: class, Phase: phase,
			OperationID: uuid.Must(uuid.NewV7()), ParentOperationID: uuid.Must(uuid.NewV7()),
			StartedAt: now.Add(-time.Second), CompletedAt: now, Target: &target, Read: nil,
		},
		Delta: nil, Outcome: outcome, Error: nil, IdempotencyKey: "", OccurredAt: now, Extra: nil,
	}
}

// userActor returns an authenticated user actor without personal data.
func userActor() audit.Actor {
	return audit.Actor{
		Type: audit.ActorUser, ID: uuid.Must(uuid.NewV7()), Email: "", Name: "",
		SessionID: "", IP: "", UserAgent: "", RequestID: "", APITokenLabel: "",
	}
}
