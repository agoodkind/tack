package integration_test

import (
	_ "embed"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auditrelay"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/testenv"
)

//go:embed testdata/runtime_audit_read_outbox_parts.sql
var readOutboxPartsStatement string

// outboxEvents decodes the outbox records of eventID in order. A plain event
// record is one event. Part messages followed by a manifest message are one
// event.
func outboxEvents(t *testing.T, records [][]byte, eventID uuid.UUID) []audit.Event {
	t.Helper()
	type outboxRecord struct {
		Message string    `json:"audit_message"`
		EventID uuid.UUID `json:"event_id"`
		Payload []byte    `json:"payload"`
	}
	var envelope []byte
	var events []audit.Event
	for _, raw := range records {
		var record outboxRecord
		if err := json.Unmarshal(raw, &record); err != nil || record.EventID != eventID {
			continue
		}
		switch record.Message {
		case "":
			var event audit.Event
			if err := json.Unmarshal(raw, &event); err != nil {
				t.Fatalf("decode the plain event %s: %v", eventID, err)
			}
			events = append(events, event)
		case "event_part":
			envelope = append(envelope, record.Payload...)
		default:
			var decoded struct {
				Event audit.Event `json:"event"`
			}
			if err := json.Unmarshal(envelope, &decoded); err != nil {
				t.Fatalf("decode the envelope of event %s: %v", eventID, err)
			}
			events = append(events, decoded.Event)
			envelope = nil
		}
	}
	return events
}

// deliverReadThroughRelay runs the real relay against the outbox of backend
// and waits until the relay removes the records of eventID.
func deliverReadThroughRelay(t *testing.T, backend *readBackend, eventID uuid.UUID) {
	t.Helper()
	stack := startRuntimeAuditStack(t)
	opened, err := auditrelay.Open(t.Context(), auditrelay.Config{
		Brokers: []string{testenv.Kafka(t)}, Topic: stack.topic,
		YugabyteDSN: testenv.Ledger(t), FDBClusterFile: backend.clusterFile, FDBTransactionTimeout: 5 * time.Second,
		PollInterval: 100 * time.Millisecond, BatchSize: 64,
	})
	if err != nil {
		t.Fatalf("open the relay: %v", err)
	}
	opened.Relay.Start(t.Context())
	defer opened.Pool.Close()
	defer func() { _ = opened.Relay.Close() }()
	deadline := clock.Now().Add(runtimeAuditDeadline)
	for len(backend.terminal(t, eventID)) > 0 {
		if clock.Now().After(deadline) {
			t.Fatalf("the relay did not deliver read %s within %s", eventID, runtimeAuditDeadline)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// yugabyteOutboxRecords returns the part messages and then the outbox record
// of eventID from public.ops_outbox_parts and public.ops_outbox.
func yugabyteOutboxRecords(t *testing.T, admin *pgxpool.Pool, eventID uuid.UUID) [][]byte {
	t.Helper()
	rows, err := admin.Query(t.Context(), readOutboxPartsStatement, eventID)
	if err != nil {
		t.Fatalf("read the YugabyteDB outbox parts: %v", err)
	}
	records, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
	if err != nil {
		t.Fatalf("scan the YugabyteDB outbox parts: %v", err)
	}
	var record []byte
	err = admin.QueryRow(t.Context(), readOutboxEventsStatement, eventID).Scan(&record)
	if errors.Is(err, pgx.ErrNoRows) {
		return records
	}
	if err != nil {
		t.Fatalf("read the YugabyteDB outbox: %v", err)
	}
	return append(records, record)
}
