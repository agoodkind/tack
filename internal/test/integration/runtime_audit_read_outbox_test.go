package integration_test

import (
	"encoding/json"
	"os"
	"path/filepath"
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

// readTestSQL reads a SQL test fixture.
func readTestSQL(t *testing.T, name string) string {
	t.Helper()
	statement, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read testdata/%s: %v", name, err)
	}
	return string(statement)
}

// outboxEvents decodes the outbox records for eventID in order. It reads each plain record
// as one event. Part messages must precede their manifest message to form one event.
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

// deliverReadThroughRelay runs the relay for backend and waits for it to delete the outbox
// records for eventID.
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
			t.Fatalf("wait for the relay to delete read %s outbox records: exceeded %s", eventID, runtimeAuditDeadline)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// yugabyteOutboxRecords returns the part messages and then the outbox record
// of eventID from public.ops_outbox_parts and public.ops_outbox. One statement
// reads both tables from one snapshot. The result contains every record of a
// committed write or none of them.
func yugabyteOutboxRecords(t *testing.T, admin *pgxpool.Pool, eventID uuid.UUID) [][]byte {
	t.Helper()
	rows, err := admin.Query(t.Context(), readTestSQL(t, "runtime_audit_read_outbox_records.sql"), eventID)
	if err != nil {
		t.Fatalf("read the YugabyteDB outbox records of event %s: %v", eventID, err)
	}
	records, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
	if err != nil {
		t.Fatalf("scan the YugabyteDB outbox records of event %s: %v", eventID, err)
	}
	return records
}
