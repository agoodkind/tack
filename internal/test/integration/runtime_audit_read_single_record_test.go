package integration_test

import (
	"encoding/json"
	"testing"

	"goodkind.io/tack/internal/audit"
)

// TestRuntimeAuditSingleRecordReadRelay finalizes a YugabyteDB operator read
// without read targets. The final event fits one outbox record, and its extra
// has several members in an order that JSONB changes. The real relay must
// deliver the event to the ledger, delete the outbox row, and mark the terminal
// receipt published.
func TestRuntimeAuditSingleRecordReadRelay(t *testing.T) {
	backend := yugabyteReadBackend(t)
	fixture := newReadFixture(t)
	fixture.admission.Template.Extra = json.RawMessage(`{"operator_source":"flag","input":{"tag":"release"},"op_id":"019dd226-440e-729a-a442-281aaf73ca30"}`)
	fixture.completion = completeRead(t, fixture.completion.Token, fixture.admission.Template, nil, "single record read payload")
	fixture.begin(t, backend.store)
	if err := backend.store.FinalizeRead(t.Context(), fixture.completion); err != nil {
		t.Fatalf("Finalizing the read failed: %v", err)
	}
	eventID := fixture.admission.Token.EventID
	encoded, err := audit.EncodeEvent(fixture.completion.Event, nil)
	if err != nil || encoded.Multipart() {
		t.Fatalf("Read %s must encode as one record: multipart %t, error %v", eventID, encoded.Multipart(), err)
	}

	pipeline := newPartsPipeline(t)
	pipeline.startConsumer(t)
	pipeline.startRelay(t, nil)
	waitFor(t, "deletion of the outbox row", func() bool { return len(backend.terminal(t, eventID)) == 0 })
	row := pipeline.stack().waitForRow(t, eventID)
	if row.Outcome != audit.OutcomeCompleted {
		t.Fatalf("The ledger row of read %s has outcome %s instead of %s", eventID, row.Outcome, audit.OutcomeCompleted)
	}
	if published := pipeline.count(t, readTestSQL(t, "runtime_audit_read_published.sql"), eventID); published != 1 {
		t.Fatalf("The relay marked %d terminal receipts published for read %s instead of 1", published, eventID)
	}
}
