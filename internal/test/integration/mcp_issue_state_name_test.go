package integration

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/testenv"
)

func TestUpdateIssueStateByNameUsesTheIssueProject(t *testing.T) {
	harness := NewMCPHarness(t)
	stores, err := fdbadapter.NewStores(testenv.FoundationDB(t), testTransactionTimeout, nil)
	if err != nil {
		t.Fatalf("open stores: %v", err)
	}
	projectID := harness.rawID(t, "tack_get_project", harness.Project)
	otherArguments := harness.projectArgs()
	otherArguments.ProjectReference = ""
	otherArguments.Name = harness.Project + "-other"
	otherArguments.Properties = datagen.NodeProperties{"identifier": json.RawMessage(strconv.Quote(harness.Project + "-other"))}
	otherProjectID := harness.Call(t, "tack_create_project", otherArguments).RawID()

	issueArguments := harness.projectArgs()
	issueArguments.Name = "Issue with a state set by name"
	issueID := harness.Call(t, "tack_create_issue", issueArguments).RawID()

	// The seed creates a "Done" state in each project. The name "Done" matches
	// a state in both projects.
	updateArguments := harness.projectArgs()
	updateArguments.ProjectReference = ""
	updateArguments.NodeID = issueID
	updateArguments.Properties = datagen.NodeProperties{"state_id": json.RawMessage(strconv.Quote("Done"))}
	harness.Call(t, "tack_update_issue", updateArguments)

	wantStateID := projectStateID(t, stores, harness, projectID, "Done")
	otherStateID := projectStateID(t, stores, harness, otherProjectID, "Done")
	view, err := stores.Views.Get(t.Context(), uuid.MustParse(issueID))
	if err != nil || view == nil {
		t.Fatalf("issue %s after the update = %v, %v", issueID, view, err)
	}
	if got := stringPropOf(view, "state_id"); got != wantStateID {
		t.Fatalf("state_id = %q, want the Done state %s of the issue project (the other project's Done state is %s)", got, wantStateID, otherStateID)
	}
}

func projectStateID(t *testing.T, stores *fdbadapter.Stores, harness *MCPHarness, projectID, name string) string {
	t.Helper()
	states, err := stores.Nodes.ListByProperty(t.Context(), harness.orgID, "state", "parent_id", jsonStr(projectID))
	if err != nil {
		t.Fatalf("list states of project %s: %v", projectID, err)
	}
	for _, state := range states {
		if state.Name == name {
			return state.ID.String()
		}
	}
	t.Fatalf("project %s has no state named %q", projectID, name)
	return ""
}
