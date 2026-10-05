package integration_test

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const stateNameTransactionTimeout = 10 * time.Second

func TestUpdateIssueStateByNameUsesTheIssueProject(t *testing.T) {
	harness := integration.NewMCPHarness(t)
	stores, err := fdbadapter.NewStores(testenv.FoundationDB(t), stateNameTransactionTimeout, nil)
	if err != nil {
		t.Fatalf("open stores: %v", err)
	}
	projectID := harness.Call(t, "tack_get_project", stateNameArguments(harness, harness.Project)).RawID()
	otherArguments := stateNameArguments(harness, "")
	otherArguments.Name = harness.Project + "-other"
	otherArguments.Properties = datagen.NodeProperties{"identifier": json.RawMessage(strconv.Quote(harness.Project + "-other"))}
	otherProjectID := harness.Call(t, "tack_create_project", otherArguments).RawID()

	issueArguments := stateNameArguments(harness, harness.Project)
	issueArguments.Name = "Issue with a state set by name"
	issueID := harness.Call(t, "tack_create_issue", issueArguments).RawID()

	// Both projects have a Done state.
	updateArguments := stateNameArguments(harness, "")
	updateArguments.NodeID = issueID
	updateArguments.Properties = datagen.NodeProperties{"state_id": json.RawMessage(strconv.Quote("Done"))}
	harness.Call(t, "tack_update_issue", updateArguments)

	view, err := stores.Views.Get(t.Context(), uuid.MustParse(issueID))
	if err != nil || view == nil {
		t.Fatalf("issue %s after the update = %v, %v", issueID, view, err)
	}
	wantStateID := doneStateID(t, stores, view.OrgID, projectID)
	otherStateID := doneStateID(t, stores, view.OrgID, otherProjectID)
	var got string
	if err := json.Unmarshal(view.Props["state_id"], &got); err != nil || got != wantStateID {
		t.Fatalf("state_id = %q, %v; want the Done state %s of the issue project (the other project's Done state is %s)",
			got, err, wantStateID, otherStateID)
	}
}

func stateNameArguments(harness *integration.MCPHarness, project string) datagen.ToolArguments {
	return datagen.ToolArguments{
		WorkspaceReference: harness.Workspace, ProjectReference: project, IssueReference: "", Name: "", Properties: nil,
		NodeID: "", Query: "", NodeType: "", Direction: "", SourceID: "", RelationType: "", TargetID: "", Limit: 0, Cursor: "",
	}
}

func doneStateID(t *testing.T, stores *fdbadapter.Stores, orgID uuid.UUID, projectID string) string {
	t.Helper()
	states, err := stores.Nodes.ListByProperty(t.Context(), orgID, "state", "parent_id", json.RawMessage(strconv.Quote(projectID)))
	if err != nil {
		t.Fatalf("list states of project %s: %v", projectID, err)
	}
	for _, state := range states {
		if state.Name == "Done" {
			return state.ID.String()
		}
	}
	t.Fatalf("project %s has no Done state", projectID)
	return ""
}
