package integration_test

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
)

// TestRuntimeAuditReadContextFields records a format 2 read event with a
// selection, an HTTP method, and a request path through the real Kafka
// consumer. The ledger reader must return the same fields, and the export
// of the organization must pass verification.
func TestRuntimeAuditReadContextFields(t *testing.T) {
	stack := startRuntimeAuditStack(t)
	oldest := clock.Now().UTC().Add(-time.Minute)
	orgID := uuid.Must(uuid.NewV7())
	target := audit.Target{
		Kind: audit.TargetKindResource, NodeType: "", Reference: "", Name: string(audit.VerbNodeRead), URI: "/mcp",
		RelationType: "", ID: uuid.Nil, SourceID: uuid.Nil, TargetID: uuid.Nil,
	}
	event := completeEvent(orgID, userActor(), target, audit.OperationRead, audit.PhaseRead, audit.OutcomeRefused)
	event.Error = &audit.EventError{Code: "invalid_reference", Message: "the request contains an invalid node reference"}
	event.Context.Selection = &audit.ReadSelection{
		Reference: "PROJ-12",
		Criteria:  map[string]json.RawMessage{"workspace_reference": json.RawMessage(`"main"`), "limit": json.RawMessage(`25`)},
	}
	event.Context.HTTPMethod, event.Context.RequestPath = http.MethodPost, "/mcp"
	stack.record(t, event)

	row := stack.waitForRow(t, event.EventID)
	if !reflect.DeepEqual(row.Context.Selection, event.Context.Selection) ||
		row.Context.HTTPMethod != http.MethodPost || row.Context.RequestPath != "/mcp" {
		t.Fatalf("stored context = %+v, want selection %+v, method POST, and path /mcp", row.Context, event.Context.Selection)
	}
	if err := stack.exportAndVerify(t, orgID, oldest, clock.Now().UTC().Add(time.Minute)); err != nil {
		t.Fatalf("export with the new context fields failed verification: %v", err)
	}
}

// TestRuntimeAuditReadSelection reads a project by UUID and sends an issue
// read with an invalid reference. Both read events must record the requested
// node reference and the workspace argument. The invalid read must record
// the invalid_reference code. Requests to an unregistered path must record
// the HTTP method beside the route pattern, and record the request path only
// when it passes the character check.
func TestRuntimeAuditReadSelection(t *testing.T) {
	server := startPublicServer(t, nil)
	identifier := "audit-sel-" + uuid.NewString()[:8]
	created := server.rpc(t, newRequestID(t, "create"), "tools/call", map[string]any{
		"name": "tack_create_project",
		"arguments": map[string]any{
			"workspace_reference": server.workspace, "name": identifier,
			"properties": map[string]any{"identifier": identifier},
		},
	})
	projectID := rawID(t, toolText(t, created))
	readID, invalidID := newRequestID(t, "selection"), newRequestID(t, "invalid")
	read := server.rpc(t, readID, "tools/call", map[string]any{
		"name": "tack_get_project", "arguments": map[string]any{"workspace_reference": server.workspace, "node_id": projectID},
	})
	invalid := server.rpc(t, invalidID, "tools/call", map[string]any{
		"name": "tack_get_issue", "arguments": map[string]any{"workspace_reference": server.workspace, "node_id": "!!invalid"},
	})
	pathID, rejectedPathID := newRequestID(t, "path"), newRequestID(t, "bad-path")
	path := server.send(t, http.MethodGet, "/unknown-route/item_1", "", pathID, nil)
	rejectedPath := server.send(t, http.MethodGet, "/unknown%20route", "", rejectedPathID, nil)

	ledger := startPublicLedger(t, server)
	readRow := ledger.row(t, server.orgID, readID)
	requireReadRow(t, readRow, audit.VerbNodeRead, audit.OutcomeCompleted, "", read.body)
	requireSelection(t, readRow, projectID, server.workspace)
	invalidRow := ledger.row(t, server.orgID, invalidID)
	requireReadRow(t, invalidRow, audit.VerbNodeRead, audit.OutcomeRefused, "invalid_reference", invalid.body)
	requireSelection(t, invalidRow, "!!invalid", server.workspace)
	if readRow.Context.HTTPMethod != http.MethodPost || readRow.Context.RequestPath != "/mcp" {
		t.Fatalf("read event %s method = %q, path = %q, want POST /mcp", readRow.EventID, readRow.Context.HTTPMethod, readRow.Context.RequestPath)
	}

	pathRow := ledger.row(t, audit.SystemOrgID(), pathID)
	requireReadRow(t, pathRow, audit.VerbHTTPRouteUnknown, audit.OutcomeRefused, "not_found", path.body)
	if pathRow.Context.HTTPMethod != http.MethodGet || pathRow.Context.RequestPath != "/unknown-route/item_1" ||
		pathRow.Context.Target == nil || pathRow.Context.Target.URI != "/" || pathRow.Context.Selection != nil {
		t.Fatalf("unknown route event %s context = %+v, want GET, the requested path, route /, and no selection", pathRow.EventID, pathRow.Context)
	}
	rejectedRow := ledger.row(t, audit.SystemOrgID(), rejectedPathID)
	requireReadRow(t, rejectedRow, audit.VerbHTTPRouteUnknown, audit.OutcomeRefused, "not_found", rejectedPath.body)
	if rejectedRow.Context.HTTPMethod != http.MethodGet || rejectedRow.Context.RequestPath != "" {
		t.Fatalf("rejected path event %s method = %q, path = %q, want GET and an empty path",
			rejectedRow.EventID, rejectedRow.Context.HTTPMethod, rejectedRow.Context.RequestPath)
	}
}

// requireSelection requires the read event to record reference as the
// requested node reference and workspace as the workspace argument.
func requireSelection(t *testing.T, row audit.Row, reference, workspace string) {
	t.Helper()
	selection := row.Context.Selection
	wantWorkspace, err := json.Marshal(workspace)
	if err != nil {
		t.Fatalf("json.Marshal failed for workspace %s: %v", workspace, err)
	}
	if selection == nil || selection.Reference != reference || string(selection.Criteria["workspace_reference"]) != string(wantWorkspace) {
		t.Fatalf("event %s selection = %+v, want reference %q and workspace %s", row.EventID, selection, reference, wantWorkspace)
	}
}
