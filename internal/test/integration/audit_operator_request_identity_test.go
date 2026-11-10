package integration_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/response"
)

const (
	failedCommandMessage   = "cli.failed"
	manifestReadFailedCode = "read_failed"
)

// TestAuditOperatorReadStoresCLIRequestIdentity checks that the permanent
// ledger row for an operator read stores the same request ID and trace ID
// as the CLI response.
func TestAuditOperatorReadStoresCLIRequestIdentity(t *testing.T) {
	ledger := startNodeAuditLedger(t)
	operatorID, orgID, eventID := uuid.Must(
		uuid.NewV7(),
	), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	run := builtAuditCommand(
		t,
		ledger.cfg.AuditReaderDSN,
		"--execute",
		"--operator-id",
		operatorID.String(),
		"--operator-email",
		"request-identity@example.test",
		"--output",
		"json",
		"ops",
		"audit",
		"manifest-parts",
		"--org",
		orgID.String(),
		"--event-id",
		eventID.String(),
	)
	if run.err != nil {
		t.Fatalf(
			"ops audit manifest-parts for event %s failed: %v\n%s",
			eventID,
			run.err,
			run.output(),
		)
	}

	var document response.JSONDocument
	if err := json.Unmarshal([]byte(run.stdout), &document); err != nil {
		t.Fatalf("decode the command output: %v\n%s", err, run.output())
	}

	if document.Meta.RequestID == "" || document.Meta.TraceID == "" {
		t.Fatalf(
			"command _meta = %+v, want a request ID and a trace ID\n%s",
			document.Meta,
			run.output(),
		)
	}

	row := operatorAuditReadRows(t, ledger, operatorID, 1)[0]
	var extra struct {
		OpID uuid.UUID `json:"op_id"`
	}
	if err := json.Unmarshal(row.Extra, &extra); err != nil {
		t.Fatalf("decode the extra of row %s: %v\n%s", row.EventID, err, row.Extra)
	}

	eventContext := row.Context
	if eventContext.RequestID != document.Meta.RequestID ||
		eventContext.TraceID != document.Meta.TraceID {
		t.Fatalf(
			"row %s request ID %q and trace ID %q, want the command _meta request ID %q and trace ID %q",
			row.EventID,
			eventContext.RequestID,
			eventContext.TraceID,
			document.Meta.RequestID,
			document.Meta.TraceID,
		)
	}

	targetURI := "tack:audit-event/" + eventID.String()
	if eventContext.OperationID == uuid.Nil || eventContext.OperationID != extra.OpID ||
		eventContext.Target == nil || eventContext.Target.URI != targetURI || row.Outcome != audit.OutcomeCompleted {
		t.Fatalf(
			"row %s operation %s (extra op %s), target %+v, outcome %q, want one operation with target %s and outcome %q",
			row.EventID,
			eventContext.OperationID,
			extra.OpID,
			eventContext.Target,
			row.Outcome,
			targetURI,
			audit.OutcomeCompleted,
		)
	}

	ledger.requireChainVerifies(t, audit.SystemOrgID())
}

// TestAuditOperatorFailedReadStoresCLIRequestIdentity makes an operator read
// fail and checks that its permanent ledger row stores the same request ID and
// trace ID that the command logged.
func TestAuditOperatorFailedReadStoresCLIRequestIdentity(t *testing.T) {
	ledger := startNodeAuditLedger(t)
	operatorID, orgID, eventID := uuid.Must(
		uuid.NewV7(),
	), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	run := builtAuditCommand(
		t,
		manifestUnreachableReaderDSN,
		"--execute",
		"--operator-id",
		operatorID.String(),
		"--operator-email",
		"request-identity@example.test",
		"--output",
		"json",
		"ops",
		"audit",
		"manifest-parts",
		"--org",
		orgID.String(),
		"--event-id",
		eventID.String(),
	)
	if run.err == nil {
		t.Fatalf(
			"ops audit manifest-parts with an unreachable reader succeeded, want a failed read\n%s",
			run.output(),
		)
	}

	requestID, traceID := failedCommandCorrelation(t, run)
	row := operatorAuditReadRows(t, ledger, operatorID, 1)[0]
	eventContext := row.Context
	if eventContext.RequestID != requestID || eventContext.TraceID != traceID {
		t.Fatalf(
			"row %s request ID %q and trace ID %q, want the failed command request ID %q and trace ID %q",
			row.EventID,
			eventContext.RequestID,
			eventContext.TraceID,
			requestID,
			traceID,
		)
	}

	targetURI := "tack:audit-event/" + eventID.String()
	if row.Outcome != audit.OutcomeFailed || row.Error == nil ||
		row.Error.Code != manifestReadFailedCode ||
		eventContext.Target == nil ||
		eventContext.Target.URI != targetURI {
		t.Fatalf(
			"row %s outcome %q, error %+v, target %+v, want outcome %q with code %s and target %s",
			row.EventID,
			row.Outcome,
			row.Error,
			eventContext.Target,
			audit.OutcomeFailed,
			manifestReadFailedCode,
			targetURI,
		)
	}
}

func failedCommandCorrelation(t *testing.T, run builtAuditRun) (string, string) {
	t.Helper()
	fields := map[string]string{}

	for line := range strings.Lines(run.log) {
		if !strings.Contains(line, " "+failedCommandMessage+" ") {
			continue
		}

		for field := range strings.FieldsSeq(line) {
			key, value, _ := strings.Cut(field, "=")
			fields[key] = value
		}
	}

	if fields["request_id"] == "" || fields["trace_id"] == "" {
		t.Fatalf(
			"the %s log line does not report a request ID and a trace ID\n%s",
			failedCommandMessage,
			run.output(),
		)
	}

	return fields["request_id"], fields["trace_id"]
}

func operatorAuditReadRows(
	t *testing.T,
	ledger *nodeAuditLedger,
	operatorID uuid.UUID,
	want int,
) []audit.Row {
	t.Helper()
	now := clock.Now()
	filter := audit.QueryFilter{
		OrgID:     audit.SystemOrgID(),
		Oldest:    now.Add(-time.Hour),
		Latest:    now.Add(time.Hour),
		Action:    string(audit.VerbAuditRead),
		ActorID:   operatorID,
		EntityID:  uuid.Nil,
		RequestID: "",
		TraceID:   "",
		Limit:     want + 1,
	}
	deadline := clock.Now().Add(nodeAuditDeadline)

	for {
		rows, err := ledger.reader.Query(t.Context(), filter)
		if err != nil {
			t.Fatalf("query the audit.read rows of operator %s: %v", operatorID, err)
		}

		if len(rows) > want {
			t.Fatalf(
				"ledger has %d audit.read rows for operator %s, want %d",
				len(rows),
				operatorID,
				want,
			)
		}

		if len(rows) == want {
			return rows
		}

		if clock.Now().After(deadline) {
			t.Fatalf(
				"ledger has %d audit.read rows for operator %s after %s, want %d",
				len(rows),
				operatorID,
				nodeAuditDeadline,
				want,
			)
		}

		time.Sleep(nodeAuditPollInterval)
	}
}
