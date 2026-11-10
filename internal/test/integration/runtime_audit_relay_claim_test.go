package integration_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auditrelay"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/testenv"
)

const (
	// relayClaimEvents sets the number of events written before the relays start.
	relayClaimEvents = 40
	// relayClaimRelays sets the number of relays that concurrently drain the
	// outbox.
	relayClaimRelays = 3
	// relayClaimBatch limits the number of outbox rows each relay claims at once.
	relayClaimBatch = 8
)

// TestRuntimeAuditRelayClaimsYugabyteRows checks that concurrent relays using
// audit_writer drain the outbox without duplicate Kafka records. It also checks
// that audit_writer cannot update the event column.
func TestRuntimeAuditRelayClaimsYugabyteRows(t *testing.T) {
	stack := startRuntimeAuditStack(t)
	ledgerDSN := testenv.Ledger(t)
	writerDSN := roleLoginDSN(t, stack.admin, ledgerDSN, auditWriterRole)
	outbox := audit.NewPoolOutbox(stack.admin)
	orgID := uuid.Must(uuid.NewV7())
	for range relayClaimEvents {
		if err := outbox.WriteOutbox(t.Context(), smallChangeEvent(orgID)); err != nil {
			t.Fatalf("Writing a YugabyteDB outbox event failed: %v", err)
		}
	}
	pending := pendingOutboxRecords(t, stack)
	for range relayClaimRelays {
		opened, err := auditrelay.Open(t.Context(), auditrelay.Config{
			Brokers: []string{testenv.Kafka(t)}, Topic: stack.topic, YugabyteDSN: writerDSN,
			FDBClusterFile: "", FDBTransactionTimeout: 0, PollInterval: 50 * time.Millisecond, BatchSize: relayClaimBatch,
		})
		if err != nil {
			t.Fatalf("Opening a relay with the audit_writer login failed: %v", err)
		}
		opened.Relay.Start(t.Context())
		t.Cleanup(opened.Pool.Close)
		t.Cleanup(func() { _ = opened.Relay.Close() })
	}
	deadline := clock.Now().Add(runtimeAuditDeadline)
	for pendingOutboxRecords(t, stack) > 0 {
		if clock.Now().After(deadline) {
			t.Fatalf("The YugabyteDB outbox did not empty before the deadline; the remaining record count is %d", pendingOutboxRecords(t, stack))
		}
		time.Sleep(100 * time.Millisecond)
	}
	marks, err := audit.TopicHighWaterMarks(t.Context(), []string{testenv.Kafka(t)}, stack.topic)
	if err != nil {
		t.Fatalf("Reading Kafka partition offsets to count published records failed: %v", err)
	}
	var published int64
	for _, mark := range marks {
		published += mark
	}
	if published != pending {
		t.Fatalf("The Kafka record count differs from the initial pending count: %d published %d pending", published, pending)
	}
	requireWriterCannotEditEvents(t, writerDSN)
}

// requireWriterCannotEditEvents checks that an audit_writer login cannot update
// public.ops_outbox.event and receives the insufficient-privilege SQLSTATE.
func requireWriterCannotEditEvents(t *testing.T, writerDSN string) {
	t.Helper()
	writer, err := pgxpool.New(t.Context(), writerDSN)
	if err != nil {
		t.Fatalf("Creating the audit_writer connection pool for the event update check failed: %v", err)
	}
	t.Cleanup(writer.Close)
	_, err = writer.Exec(t.Context(), readTestSQL(t, "runtime_audit_relay_claim_update_event.sql"), uuid.Must(uuid.NewV7()))
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) || postgresError.Code != insufficientPrivilegeState {
		t.Fatalf("The audit_writer event update returned an unexpected result; the actual error and required SQLSTATE are %v %s", err, insufficientPrivilegeState)
	}
}

// pendingOutboxRecords counts pending Kafka records as outbox rows plus parts.
// Each row contributes a plain event or manifest, and each part contributes one
// additional record.
func pendingOutboxRecords(t *testing.T, stack runtimeAuditStack) int64 {
	t.Helper()
	var records int64
	if err := stack.admin.QueryRow(t.Context(), readTestSQL(t, "runtime_audit_outbox_records.sql")).Scan(&records); err != nil {
		t.Fatalf("Counting pending YugabyteDB outbox rows and parts failed: %v", err)
	}
	return records
}
