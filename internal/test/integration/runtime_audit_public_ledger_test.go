package integration_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auditrelay"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/testenv"
)

// publicLedger runs the relay and the audit consumer for the FoundationDB
// outbox of one public server.
type publicLedger struct {
	stack runtimeAuditStack
	since time.Time
}

// startPublicLedger starts the audit consumer on a new topic and the relay
// for the server's FoundationDB outbox.
func startPublicLedger(t *testing.T, server *publicServer) publicLedger {
	t.Helper()
	return startPublicLedgerOn(t, server, startRuntimeAuditStack(t))
}

// startPublicLedgerOn starts the relay for the server's FoundationDB outbox
// on the topic of stack, which runs the audit consumer.
func startPublicLedgerOn(t *testing.T, server *publicServer, stack runtimeAuditStack) publicLedger {
	t.Helper()
	since := clock.Now().Add(-time.Hour)
	opened, err := auditrelay.Open(t.Context(), auditrelay.Config{
		Brokers: []string{testenv.Kafka(t)}, Topic: stack.topic,
		YugabyteDSN: testenv.Ledger(t), FDBClusterFile: server.clusterFile, FDBTransactionTimeout: 5 * time.Second,
		PollInterval: 100 * time.Millisecond, BatchSize: 64,
	})
	if err != nil {
		t.Fatalf("auditrelay.Open failed: %v", err)
	}
	opened.Relay.Start(t.Context())
	t.Cleanup(opened.Pool.Close)
	t.Cleanup(func() { _ = opened.Relay.Close() })
	return publicLedger{stack: stack, since: since}
}

// row waits until the ledger contains exactly one event for requestID in
// orgID and returns it.
func (ledger publicLedger) row(t *testing.T, orgID uuid.UUID, requestID string) audit.Row {
	t.Helper()
	deadline := clock.Now().Add(publicDeliveryTimeout)
	for {
		rows, err := ledger.stack.reader.Query(t.Context(), audit.QueryFilter{
			OrgID: orgID, Oldest: ledger.since, Latest: clock.Now().Add(time.Hour), Action: "",
			ActorID: uuid.Nil, EntityID: uuid.Nil, RequestID: requestID, TraceID: "", Limit: 10,
		})
		if err != nil {
			t.Fatalf("ledger query failed for request %s: %v", requestID, err)
		}
		if len(rows) > 1 {
			t.Fatalf("ledger has %d events for request %s, want 1", len(rows), requestID)
		}
		if len(rows) == 1 {
			return rows[0]
		}
		if clock.Now().After(deadline) {
			t.Fatalf("ledger did not store an event for request %s within %s", requestID, publicDeliveryTimeout)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// requestRows waits until ready accepts the ledger events for requestID in
// orgID and returns them.
func (ledger publicLedger) requestRows(t *testing.T, orgID uuid.UUID, requestID string, ready func([]audit.Row) bool) []audit.Row {
	t.Helper()
	deadline := clock.Now().Add(publicDeliveryTimeout)
	for {
		rows, err := ledger.stack.reader.Query(t.Context(), audit.QueryFilter{
			OrgID: orgID, Oldest: ledger.since, Latest: clock.Now().Add(time.Hour), Action: "",
			ActorID: uuid.Nil, EntityID: uuid.Nil, RequestID: requestID, TraceID: "", Limit: 100,
		})
		if err != nil {
			t.Fatalf("ledger query failed for request %s: %v", requestID, err)
		}
		if ready(rows) {
			return rows
		}
		if clock.Now().After(deadline) {
			t.Fatalf("ledger events for request %s after %s = %+v, want the expected events", requestID, publicDeliveryTimeout, rows)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// changeRows queries the ledger for requestID in orgID until it finds a row
// with OperationClass audit.OperationDatabaseChange or publicDeliveryTimeout passes.
// It returns only rows with that operation class and fails the test on timeout.
func (ledger publicLedger) changeRows(t *testing.T, orgID uuid.UUID, requestID string) []audit.Row {
	t.Helper()
	deadline := clock.Now().Add(publicDeliveryTimeout)
	for {
		rows, err := ledger.stack.reader.Query(t.Context(), audit.QueryFilter{
			OrgID: orgID, Oldest: ledger.since, Latest: clock.Now().Add(time.Hour), Action: "",
			ActorID: uuid.Nil, EntityID: uuid.Nil, RequestID: requestID, TraceID: "", Limit: 100,
		})
		if err != nil {
			t.Fatalf("ledger query failed while waiting for a change event for request %s: %v", requestID, err)
		}
		changes := make([]audit.Row, 0, len(rows))
		for _, row := range rows {
			if row.Context.OperationClass == audit.OperationDatabaseChange {
				changes = append(changes, row)
			}
		}
		if len(changes) > 0 {
			return changes
		}
		if clock.Now().After(deadline) {
			t.Fatalf("ledger query did not return a database change event for request %s within %s", requestID, publicDeliveryTimeout)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// manifestTargets returns the ordered read targets of the event in the ledger.
func (ledger publicLedger) manifestTargets(t *testing.T, row audit.Row) []audit.Target {
	t.Helper()
	parts, err := ledger.stack.reader.ManifestParts(t.Context(), row.OrgID, row.EventID)
	if err != nil {
		t.Fatalf("ManifestParts failed for event %s: %v", row.EventID, err)
	}
	var targets []audit.Target
	for _, part := range parts {
		var decoded []audit.Target
		if err := json.Unmarshal(part.Payload, &decoded); err != nil {
			t.Fatalf("json.Unmarshal failed for manifest part %d of event %s: %v", part.Index, row.EventID, err)
		}
		targets = append(targets, decoded...)
	}
	return targets
}

// requireReadRow checks one read event against the expected verb, outcome, failure code, and payload digest.
// When code is empty, it requires row.Error to be nil.
func requireReadRow(t *testing.T, row audit.Row, verb audit.Verb, outcome audit.Outcome, code string, body []byte) {
	t.Helper()
	if row.Action != string(verb) || row.Outcome != outcome {
		t.Fatalf("event %s = %s %s, want %s %s", row.EventID, row.Action, row.Outcome, verb, outcome)
	}
	eventContext := row.Context
	if eventContext.AuditFormatVersion != audit.AuditFormatComplete || eventContext.OperationClass != audit.OperationRead ||
		eventContext.Phase != audit.PhaseRead || eventContext.OperationID == uuid.Nil {
		t.Fatalf("event %s context = %+v, want a format 2 read with an operation ID", row.EventID, eventContext)
	}
	switch {
	case code == "" && row.Error != nil:
		t.Fatalf("event %s error = %+v, want nil", row.EventID, row.Error)
	case code != "" && (row.Error == nil || row.Error.Code != code):
		t.Fatalf("event %s error = %+v, want code %s", row.EventID, row.Error, code)
	}
	if eventContext.Read == nil || eventContext.Read.PayloadSHA256 != payloadDigest(body) {
		t.Fatalf("event %s read evidence = %+v, want payload digest %s", row.EventID, eventContext.Read, payloadDigest(body))
	}
}
