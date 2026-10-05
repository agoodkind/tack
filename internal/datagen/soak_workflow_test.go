package datagen

import (
	"fmt"
	"testing"

	"goodkind.io/tack/internal/clock"
)

func TestWorkflowVisitsEveryIssueInEveryProject(t *testing.T) {
	t.Parallel()
	const projectCount = 8
	const issueCount = 16
	content, err := NewContent(t.Context(), 245)
	if err != nil {
		t.Fatalf("NewContent() error = %v", err)
	}
	soak := &Soak{driver: NewDriver(nil, true, 245), content: content, clock: clock.Wall{}}
	for projectIndex := range projectCount {
		project := &soakProject{
			Workspace: WorkspaceIdentity{Actors: []Actor{{Token: "token"}}},
			States:    []soakNode{{RawID: "state", Group: stateGroupCompleted}},
		}
		for issueIndex := range issueCount {
			project.Issues = append(project.Issues, &soakIssue{
				soakNode: soakNode{RawID: fmt.Sprintf("p%d-i%d", projectIndex, issueIndex)},
			})
		}
		soak.projects = append(soak.projects, project)
	}
	for workflowTick := range projectCount * issueCount {
		operationIndex := workflowTick * soakOperationKinds
		if err := soak.executeOperation(t.Context(), operationIndex); err != nil {
			t.Fatalf("executeOperation(%d) error = %v", operationIndex, err)
		}
	}
	for projectIndex, project := range soak.projects {
		for issueIndex, issue := range project.Issues {
			if issue.Stage != 1 {
				t.Fatalf(
					"project %d issue %d stage = %d, want 1",
					projectIndex,
					issueIndex,
					issue.Stage,
				)
			}
		}
	}
}

func TestWorkflowMovesThroughRealStateIndexesBeforeReopen(t *testing.T) {
	t.Parallel()
	issue := &soakIssue{Reopen: true}
	want := []workflowAction{
		{Kind: workflowSetState, StateIndex: 0},
		{Kind: workflowAssign},
		{Kind: workflowLabel},
		{Kind: workflowComment},
		{Kind: workflowSetState, StateIndex: 1},
		{Kind: workflowSetState, StateIndex: 2},
		{Kind: workflowSetState, StateIndex: 0},
	}
	for index, expected := range want {
		got := nextWorkflowAction(issue, 3)
		if got != expected {
			t.Fatalf("action %d = %#v, want %#v", index, got, expected)
		}
	}
}

func TestWorkflowKeepsClosedIssueActiveWithComments(t *testing.T) {
	t.Parallel()
	issue := &soakIssue{Stage: 6, Reopen: false}
	if got := nextWorkflowAction(issue, 3); got.Kind != workflowComment {
		t.Fatalf("closed action = %#v, want comment", got)
	}
	if issue.Stage != 6 {
		t.Fatalf("closed stage = %d, want 6", issue.Stage)
	}
}

func TestWorkflowStatesExcludeCancelledAndUseSortOrder(t *testing.T) {
	t.Parallel()
	project := &soakProject{States: []soakNode{
		{RawID: "done", Group: "completed", SortOrder: 30},
		{RawID: "cancelled", Group: "cancelled", SortOrder: 40},
		{RawID: "open", Group: "unstarted", SortOrder: 10},
		{RawID: "active", Group: "started", SortOrder: 20},
	}}
	states := project.workflowStates()
	if len(states) != 3 {
		t.Fatalf("workflow states = %d, want 3", len(states))
	}
	if states[0].RawID != "open" || states[2].RawID != "done" {
		t.Fatalf("workflow state order = %#v", states)
	}
}
