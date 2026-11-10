package integration

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/searchaccess"
	"goodkind.io/tack/internal/testenv"
)

// TestDeleteCycleMovesItsIssueToTheProject creates an issue with a cycle as
// its parent and two comments under the issue, then deletes the cycle
// through tack_delete_cycle. The issue type is allowed under the project,
// the cycle's own parent. The delete must move the issue there: the issue
// keeps its comments, has exactly one child_of edge, to the project, and a
// parent_id that matches it, and search access compiles for it. The move
// must write one node.update ledger row for the issue. A later project
// delete must delete the issue and its comments.
func TestDeleteCycleMovesItsIssueToTheProject(t *testing.T) {
	harness := NewMCPHarness(t)
	stores, err := fdbadapter.NewStores(testenv.FoundationDB(t), testTransactionTimeout, nil)
	if err != nil {
		t.Fatalf("open stores: %v", err)
	}
	projectID := harness.rawID(t, "tack_get_project", harness.Project)
	cycleArguments := harness.projectArgs()
	cycleArguments.Name = "Doomed cycle"
	cycle := harness.Call(t, "tack_create_cycle", cycleArguments).RawID()
	issueArguments := harness.projectArgs()
	issueArguments.Name = "Issue in the cycle"
	issueArguments.Properties = datagen.NodeProperties{"parent_id": json.RawMessage(strconv.Quote(cycle))}
	issue := harness.Call(t, "tack_create_issue", issueArguments).RawID()
	comments := make([]uuid.UUID, 0, 2)
	for _, name := range []string{"First comment", "Second comment"} {
		commentArguments := harness.projectArgs()
		commentArguments.IssueReference = issue
		commentArguments.Name = name
		comments = append(comments, uuid.MustParse(harness.Call(t, "tack_create_comment", commentArguments).RawID()))
	}

	deleteArguments := harness.projectArgs()
	deleteArguments.ProjectReference = ""
	deleteArguments.NodeID = cycle
	text := harness.Call(t, "tack_delete_cycle", deleteArguments).Text()
	for _, want := range []string{"- Deleted nodes: 1", "- Moved nodes: 1", "- Delete state: finished"} {
		if !strings.Contains(text, want) {
			t.Errorf("cycle delete output does not contain %q:\n%s", want, text)
		}
	}

	issueID := uuid.MustParse(issue)
	view, err := stores.Views.Get(t.Context(), issueID)
	if err != nil || view == nil {
		t.Fatalf("issue %s after the cycle delete = %v, %v; want the moved issue", issueID, view, err)
	}
	if got := stringPropOf(view, "parent_id"); got != projectID {
		t.Errorf("parent_id of the moved issue = %q, want the project %s", got, projectID)
	}
	parents, err := stores.Relationships.ListBySource(t.Context(), harness.orgID, issueID, node.RelChildOf)
	if err != nil || len(parents) != 1 || parents[0].TargetID.String() != projectID {
		t.Errorf("child_of edges of the moved issue = %d, %v; want one edge to the project %s", len(parents), err, projectID)
	}
	for _, commentID := range comments {
		if kept, err := stores.Views.Get(t.Context(), commentID); err != nil || kept == nil {
			t.Errorf("comment %s after the cycle delete = %v, %v; want it kept under the moved issue", commentID, kept, err)
		}
	}
	if _, err := stores.SearchPolicySet().Index(t.Context(), searchaccess.IndexAccessRequest{
		Version: searchaccess.StableVersion, OrganizationID: harness.orgID, ResourceID: issueID, Generation: 1,
	}); err != nil {
		t.Errorf("search access of the moved issue: %v", err)
	}
	requireMoveEvent(t, stores, issueID)

	projectArguments := harness.projectArgs()
	projectArguments.ProjectReference = ""
	projectArguments.NodeID = harness.Project
	harness.Call(t, "tack_delete_project", projectArguments)
	for _, removed := range append([]uuid.UUID{issueID}, comments...) {
		if gone, err := stores.Views.Get(t.Context(), removed); err != nil || gone != nil {
			t.Errorf("node %s after the project delete = %v, %v; want it deleted", removed, gone, err)
		}
	}
}

// requireMoveEvent requires one node.update outbox row for nodeID from the
// tack_delete_cycle tool.
func requireMoveEvent(t *testing.T, stores *fdbadapter.Stores, nodeID uuid.UUID) {
	t.Helper()
	found := 0
	var mark []byte
	for {
		entries, err := stores.OpsOutbox.ReadOutboxFrom(t.Context(), mark, 500)
		if err != nil {
			t.Fatalf("read the outbox: %v", err)
		}
		if len(entries) == 0 {
			break
		}
		for _, entry := range entries {
			var event audit.Event
			if err := json.Unmarshal(entry.Event, &event); err != nil {
				t.Fatalf("decode an outbox row: %v", err)
			}
			if event.Entity.ID == nodeID && event.Verb == string(audit.VerbNodeUpdate) && event.Context.Tool == "tack_delete_cycle" {
				found++
			}
		}
		mark = entries[len(entries)-1].Mark
	}
	if found != 1 {
		t.Errorf("node.update rows for moved node %s from tack_delete_cycle = %d, want 1", nodeID, found)
	}
}
