package integration_test

import (
	"encoding/json"
	"slices"
	"strconv"
	"testing"

	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

// auditedIssueFields represents the primary record fields that the test
// changes: the issue's name and description property.
type auditedIssueFields struct {
	Name  *string                    `json:"name"`
	Props map[string]json.RawMessage `json:"props"`
}

// auditedDescription is the JSON value of the description at issue creation.
var auditedDescription = strconv.Quote("first description")

// TestNodeIntegrityAuditRecordsCommittedDeltaInLedger creates an issue
// through the MCP handler, then updates it to rename the issue and remove its
// description. The production relay, Kafka, and audit consumer write both
// events to the ledger. The test reads the events through a login that
// inherits only audit_reader and checks the before and after values, the
// removed description in Before only, and the calling user as actor. It also
// verifies that a second delivery of the update event leaves one ledger row
// and that the exported organization ledger passes chain verification.
func TestNodeIntegrityAuditRecordsCommittedDeltaInLedger(t *testing.T) {
	harness := integration.NewAuditedMCPHarness(t)
	stores, err := fdbadapter.NewStores(testenv.FoundationDB(t), nodeAuditTransactionTimeout, nil)
	if err != nil {
		t.Fatalf("open stores: %v", err)
	}
	createArguments := auditedIssueArguments(harness)
	createArguments.ProjectReference = harness.Project
	createArguments.Name = "audited issue before"
	createArguments.Properties = datagen.NodeProperties{"description": json.RawMessage(auditedDescription)}
	issueID := uuid.MustParse(harness.Call(t, "tack_create_issue", createArguments).RawID())
	updateArguments := auditedIssueArguments(harness)
	updateArguments.NodeID = issueID.String()
	updateArguments.Name = "audited issue after"
	updateArguments.Properties = datagen.NodeProperties{"description": json.RawMessage("null")}
	harness.Call(t, "tack_update_issue", updateArguments)
	view, err := stores.Views.Get(t.Context(), issueID)
	if err != nil || view == nil {
		t.Fatalf("issue %s after the update = %v, %v", issueID, view, err)
	}
	if _, kept := view.Props["description"]; kept || view.Name != "audited issue after" {
		t.Fatalf("issue %s after the update = %q with props %v, want the new name and no description", issueID, view.Name, view.Props)
	}
	updatePayload := outboxEvent(t, stores, issueID, audit.VerbNodeUpdate)

	ledger := startNodeAuditLedger(t)
	created := ledger.entityRows(t, view.OrgID, issueID, audit.VerbNodeCreate, 1)
	updated := ledger.entityRows(t, view.OrgID, issueID, audit.VerbNodeUpdate, 1)
	requireCreateDelta(t, created[0])
	requireUpdateDelta(t, updated[0])
	for _, row := range []audit.Row{created[0], updated[0]} {
		if row.ActorID != view.UpdatedBy || row.ActorKind != 1 || row.Context.Source != audit.SourceMCP {
			t.Fatalf("%s row actor = %s (kind %d, source %q), want user %s through mcp", row.Action, row.ActorID, row.ActorKind, row.Context.Source, view.UpdatedBy)
		}
	}

	ledger.redeliver(t, updatePayload)
	again := ledger.entityRows(t, view.OrgID, issueID, audit.VerbNodeUpdate, 1)
	if len(again) != 1 || again[0].EventID != updated[0].EventID || again[0].Seq != updated[0].Seq {
		t.Fatalf("update rows after a second delivery = %+v, want only row %s", again, updated[0].EventID)
	}
	ledger.requireChainVerifies(t, view.OrgID)
}

// auditedIssueArguments returns tool arguments that select the harness
// workspace and leave every other field empty.
func auditedIssueArguments(harness *integration.MCPHarness) datagen.ToolArguments {
	return datagen.ToolArguments{
		WorkspaceReference: harness.Workspace, ProjectReference: "", IssueReference: "", Name: "", Properties: nil,
		NodeID: "", Query: "", NodeType: "", Direction: "", SourceID: "", RelationType: "", TargetID: "", Limit: 0, Cursor: "",
	}
}

// requireCreateDelta checks that the create row lists the name and
// description as changed, includes their values in After, and leaves Before
// empty.
func requireCreateDelta(t *testing.T, row audit.Row) {
	t.Helper()
	if row.Delta == nil || len(row.Delta.Before) != 0 {
		t.Fatalf("create delta = %+v, want a delta without Before", row.Delta)
	}
	if !slices.Contains(row.Delta.Changed, "/name") || !slices.Contains(row.Delta.Changed, "/props/description") {
		t.Fatalf("create changed = %v, want /name and /props/description", row.Delta.Changed)
	}
	after := decodeAuditedIssueFields(t, row.Delta.After)
	if after.Name == nil || *after.Name != "audited issue before" || string(after.Props["description"]) != auditedDescription {
		t.Fatalf("create after = %s, want the created name and description", row.Delta.After)
	}
}

// requireUpdateDelta checks that the update row lists exactly the name and
// removed description as changed, includes both old values in Before, and
// includes only the new name in After.
func requireUpdateDelta(t *testing.T, row audit.Row) {
	t.Helper()
	if row.Delta == nil || !slices.Equal(row.Delta.Changed, []string{"/name", "/props/description"}) {
		t.Fatalf("update delta = %+v, want /name and /props/description changed", row.Delta)
	}
	before := decodeAuditedIssueFields(t, row.Delta.Before)
	if before.Name == nil || *before.Name != "audited issue before" || len(before.Props) != 1 || string(before.Props["description"]) != auditedDescription {
		t.Fatalf("update before = %s, want the old name and the removed description", row.Delta.Before)
	}
	after := decodeAuditedIssueFields(t, row.Delta.After)
	if after.Name == nil || *after.Name != "audited issue after" || len(after.Props) != 0 {
		t.Fatalf("update after = %s, want only the new name", row.Delta.After)
	}
}

func decodeAuditedIssueFields(t *testing.T, raw json.RawMessage) auditedIssueFields {
	t.Helper()
	var fields auditedIssueFields
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("decode delta fields %s: %v", raw, err)
	}
	return fields
}
