package integration_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/testenv"
)

// startRecordedPublicServer starts the audit consumer on a new topic and a
// public server that publishes its recorder events to that topic. It returns
// the server and a ledger that also relays the server's FoundationDB outbox.
func startRecordedPublicServer(t *testing.T) (*publicServer, publicLedger) {
	t.Helper()
	stack := startRuntimeAuditStack(t)
	cfg := publicConfig(t)
	cfg.AuditKafkaBrokers, cfg.AuditKafkaTopic = testenv.Kafka(t), stack.topic
	cfg.AuditWriterDSN, cfg.AuditAllowUnrecorded = "", false
	cfg.AuditReadFlushInterval = 10 * time.Millisecond
	server := buildPublicServer(t, cfg, nil)
	return server, startPublicLedgerOn(t, server, stack)
}

// TestRuntimeAuditPublicMutationResults creates a project, repeats the create
// with the same operation ID, and removes a relationship that does not exist.
// Every request must record a format 2 auth.token_used event with the request
// operation ID. The create must record change events under that operation
// and no mcp.tool_invoked event. The repeated create must record one
// completed node.create result with the idempotent_match reason and the
// project target. The removal must state that it removed no edge and record
// one refused relationship.remove result with the not_found code and the
// relationship target.
func TestRuntimeAuditPublicMutationResults(t *testing.T) {
	server, ledger := startRecordedPublicServer(t)
	identifier := "audit-mut-" + uuid.NewString()[:8]
	create := map[string]any{
		"name": "tack_create_project",
		"arguments": map[string]any{
			"workspace_reference": server.workspace, "name": identifier,
			"properties": map[string]any{"identifier": identifier},
		},
		"_meta": map[string]any{"tackOperationID": "op-" + uuid.NewString()},
	}
	createID, repeatID, removeID := newRequestID(t, "create"), newRequestID(t, "repeat"), newRequestID(t, "remove")
	projectID := uuid.MustParse(rawID(t, toolText(t, server.rpc(t, createID, "tools/call", create))))
	if repeated := rawID(t, toolText(t, server.rpc(t, repeatID, "tools/call", create))); repeated != projectID.String() {
		t.Fatalf("repeated create returned node %s, want project %s", repeated, projectID)
	}
	otherIdentifier := "audit-oth-" + uuid.NewString()[:8]
	other := server.rpc(t, newRequestID(t, "other"), "tools/call", map[string]any{
		"name": "tack_create_project",
		"arguments": map[string]any{
			"workspace_reference": server.workspace, "name": otherIdentifier,
			"properties": map[string]any{"identifier": otherIdentifier},
		},
	})
	otherID := uuid.MustParse(rawID(t, toolText(t, other)))
	removed := toolText(t, server.rpc(t, removeID, "tools/call", map[string]any{
		"name": "tack_remove_relationship",
		"arguments": map[string]any{
			"source_id": projectID.String(), "relation_type": "watches", "target_id": otherID.String(),
		},
	}))
	if !strings.Contains(removed, "Did not remove relationship") {
		t.Fatalf("tack_remove_relationship response = %q, want a statement that it removed no edge", removed)
	}

	createRows := ledger.requestRows(t, server.orgID, createID, hasAuthAndOther)
	createAuth := requireTokenUsedRow(t, createRows, server.userID)
	for _, row := range createRows {
		if row.Action == string(audit.VerbAuthTokenUsed) {
			continue
		}
		requireOperationChild(t, row, createAuth)
		if row.Context.Phase != audit.PhaseChange {
			t.Fatalf("create event %s = %s phase %s, want only change events beside auth.token_used", row.EventID, row.Action, row.Context.Phase)
		}
	}

	repeatRow := onlyResultRow(t, ledger.requestRows(t, server.orgID, repeatID, hasAuthAndOther), server.userID)
	requireResultRow(t, repeatRow, audit.VerbNodeCreate, audit.OutcomeCompleted, "")
	if repeatRow.Context.Reason != "idempotent_match" || repeatRow.Context.Target == nil ||
		repeatRow.Context.Target.Kind != audit.TargetKindNode || repeatRow.Context.Target.ID != projectID || repeatRow.Delta != nil {
		t.Fatalf("repeated create event %s reason = %q, target = %+v, delta = %+v; want idempotent_match, project %s, and no delta",
			repeatRow.EventID, repeatRow.Context.Reason, repeatRow.Context.Target, repeatRow.Delta, projectID)
	}

	removeRow := onlyResultRow(t, ledger.requestRows(t, server.orgID, removeID, hasAuthAndOther), server.userID)
	requireResultRow(t, removeRow, audit.VerbRelationshipRemove, audit.OutcomeRefused, "not_found")
	target := removeRow.Context.Target
	if target == nil || target.Kind != audit.TargetKindRelationship || target.SourceID != projectID ||
		target.RelationType != "watches" || target.TargetID != otherID {
		t.Fatalf("removal event %s target = %+v, want relationship %s watches %s", removeRow.EventID, target, projectID, otherID)
	}
}

// hasAuthAndOther reports whether rows contain an auth.token_used event and
// at least one other event.
func hasAuthAndOther(rows []audit.Row) bool {
	isAuth := func(row audit.Row) bool { return row.Action == string(audit.VerbAuthTokenUsed) }
	return slices.ContainsFunc(rows, isAuth) && slices.ContainsFunc(rows, func(row audit.Row) bool { return !isAuth(row) })
}

// requireTokenUsedRow returns the one auth.token_used event in rows. It
// requires a format 2 completed event for userID with an operation ID.
func requireTokenUsedRow(t *testing.T, rows []audit.Row, userID uuid.UUID) audit.Row {
	t.Helper()
	var found []audit.Row
	for _, row := range rows {
		if row.Action == string(audit.VerbMCPToolInvoked) || row.Context.AuditFormatVersion != audit.AuditFormatComplete {
			t.Fatalf("request event %s = %s in format %d, want only format 2 events without mcp.tool_invoked",
				row.EventID, row.Action, row.Context.AuditFormatVersion)
		}
		if row.Action == string(audit.VerbAuthTokenUsed) {
			found = append(found, row)
		}
	}
	if len(found) != 1 || found[0].ActorID != userID || found[0].Outcome != audit.OutcomeCompleted ||
		found[0].Context.OperationID == uuid.Nil || found[0].Context.Target == nil {
		t.Fatalf("auth.token_used events = %+v, want one completed event for %s with an operation ID and a target", found, userID)
	}
	return found[0]
}

// onlyResultRow requires rows to contain the auth.token_used event and one
// other event under its operation, and returns the other event.
func onlyResultRow(t *testing.T, rows []audit.Row, userID uuid.UUID) audit.Row {
	t.Helper()
	authRow := requireTokenUsedRow(t, rows, userID)
	if len(rows) != 2 {
		t.Fatalf("request events = %+v, want auth.token_used and one result event", rows)
	}
	for _, row := range rows {
		if row.EventID != authRow.EventID {
			requireOperationChild(t, row, authRow)
			return row
		}
	}
	return audit.Row{}
}

// requireOperationChild requires row to be a database change event of a tool
// call under the request operation of authRow.
func requireOperationChild(t *testing.T, row, authRow audit.Row) {
	t.Helper()
	if row.Context.OperationClass != audit.OperationDatabaseChange || row.Context.ParentOperationID != authRow.Context.OperationID {
		t.Fatalf("event %s class = %s, parent operation = %s; want a database change under operation %s",
			row.EventID, row.Context.OperationClass, row.Context.ParentOperationID, authRow.Context.OperationID)
	}
}

// requireResultRow checks the verb, phase, outcome, and error code of one
// result event. An empty code requires a nil error.
func requireResultRow(t *testing.T, row audit.Row, verb audit.Verb, outcome audit.Outcome, code string) {
	t.Helper()
	codeMatches := (code == "" && row.Error == nil) || (code != "" && row.Error != nil && row.Error.Code == code)
	if row.Action != string(verb) || row.Context.Phase != audit.PhaseResult || row.Outcome != outcome || !codeMatches {
		t.Fatalf("result event %s = %s %s %s error %+v, want %s result %s with code %q",
			row.EventID, row.Action, row.Context.Phase, row.Outcome, row.Error, verb, outcome, code)
	}
}
