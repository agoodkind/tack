package integration_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/response"
)

// TestAuditOperatorReadStoresCLIRequestIdentity checks that the ledger row of an operator read stores the request ID and trace ID of the CLI response.
func TestAuditOperatorReadStoresCLIRequestIdentity(t *testing.T) {
	ledger := startNodeAuditLedger(t)
	operatorID, orgID, eventID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	run := builtAuditCommand(t, ledger.cfg.AuditReaderDSN,
		"--execute", "--operator-id", operatorID.String(), "--operator-email", "request-identity@example.test",
		"--output", "json", "ops", "audit", "manifest-parts", "--org", orgID.String(), "--event-id", eventID.String())
	if run.err != nil {
		t.Fatalf("ops audit manifest-parts for event %s failed: %v\n%s", eventID, run.err, run.output())
	}
	var document response.JSONDocument
	if err := json.Unmarshal([]byte(run.stdout), &document); err != nil {
		t.Fatalf("decode the command output: %v\n%s", err, run.output())
	}
	if document.Meta.RequestID == "" || document.Meta.TraceID == "" {
		t.Fatalf("command _meta = %+v, want a request ID and a trace ID\n%s", document.Meta, run.output())
	}

	row := operatorAuditReadRow(t, ledger, operatorID)
	var extra struct {
		OpID uuid.UUID `json:"op_id"`
	}
	if err := json.Unmarshal(row.Extra, &extra); err != nil {
		t.Fatalf("decode the extra of row %s: %v\n%s", row.EventID, err, row.Extra)
	}
	eventContext := row.Context
	if eventContext.RequestID != document.Meta.RequestID || eventContext.TraceID != document.Meta.TraceID {
		t.Fatalf("row %s request ID %q and trace ID %q, want the command _meta request ID %q and trace ID %q",
			row.EventID, eventContext.RequestID, eventContext.TraceID, document.Meta.RequestID, document.Meta.TraceID)
	}
	targetURI := "tack:audit-event/" + eventID.String()
	if eventContext.OperationID == uuid.Nil || eventContext.OperationID != extra.OpID ||
		eventContext.Target == nil || eventContext.Target.URI != targetURI || row.Outcome != audit.OutcomeCompleted {
		t.Fatalf("row %s operation %s (extra op %s), target %+v, outcome %q, want one operation with target %s and outcome %q",
			row.EventID, eventContext.OperationID, extra.OpID, eventContext.Target, row.Outcome, targetURI, audit.OutcomeCompleted)
	}
	correlated := correlatedRows(t, ledger, document.Meta)
	if len(correlated) != 1 || correlated[0].EventID != row.EventID || correlated[0].Context.OperationID != eventContext.OperationID {
		t.Fatalf("rows for request %s and trace %s = %+v, want only row %s", document.Meta.RequestID, document.Meta.TraceID, correlated, row.EventID)
	}
}

func operatorAuditReadRow(t *testing.T, ledger *nodeAuditLedger, operatorID uuid.UUID) audit.Row {
	t.Helper()
	now := clock.Now()
	filter := audit.QueryFilter{
		OrgID: audit.SystemOrgID(), Oldest: now.Add(-time.Hour), Latest: now.Add(time.Hour),
		Action: string(audit.VerbAuditRead), ActorID: operatorID, EntityID: uuid.Nil, RequestID: "", TraceID: "", Limit: 2,
	}
	deadline := clock.Now().Add(nodeAuditDeadline)
	for {
		rows, err := ledger.reader.Query(t.Context(), filter)
		if err != nil {
			t.Fatalf("query the audit.read rows of operator %s: %v", operatorID, err)
		}
		if len(rows) > 1 {
			t.Fatalf("ledger has %d audit.read rows for operator %s, want 1", len(rows), operatorID)
		}
		if len(rows) == 1 {
			return rows[0]
		}
		if clock.Now().After(deadline) {
			t.Fatalf("ledger does not have an audit.read row for operator %s after %s", operatorID, nodeAuditDeadline)
		}
		time.Sleep(nodeAuditPollInterval)
	}
}

func correlatedRows(t *testing.T, ledger *nodeAuditLedger, meta response.Metadata) []audit.Row {
	t.Helper()
	now := clock.Now()
	rows, err := ledger.reader.Query(t.Context(), audit.QueryFilter{
		OrgID: audit.SystemOrgID(), Oldest: now.Add(-time.Hour), Latest: now.Add(time.Hour), Action: "",
		ActorID: uuid.Nil, EntityID: uuid.Nil, RequestID: meta.RequestID, TraceID: meta.TraceID, Limit: 2,
	})
	if err != nil {
		t.Fatalf("query the rows of request %s and trace %s: %v", meta.RequestID, meta.TraceID, err)
	}
	return rows
}
