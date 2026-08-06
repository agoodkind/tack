package datagen

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

func TestWorkflowResolvesStateWithinIssueProject(t *testing.T) {
	projectAID := deterministicUUID("project-a").String()
	projectBID := deterministicUUID("project-b").String()
	issueID := deterministicUUID("project-a-issue").String()
	projectAStateID := deterministicUUID("project-a-todo").String()
	projectBStateID := deterministicUUID("project-b-todo").String()
	fake := &workflowValidationMCP{
		issueProjects: map[string]string{issueID: projectAID},
		states: map[string]workflowValidationState{
			projectAStateID: {projectID: projectAID, name: "Todo"},
			projectBStateID: {projectID: projectBID, name: "Todo"},
		},
	}
	soak := &Soak{driver: NewDriver(testGraph(fake), false, 777)}
	project := &soakProject{
		Workspace: WorkspaceIdentity{Slug: "qa-777", Actors: []Actor{{Token: "token"}}},
		States: []soakNode{{
			RawID: projectBStateID, Name: "Todo", Group: stateGroupUnstarted,
		}},
		Issues: []*soakIssue{{soakNode: soakNode{RawID: issueID}}},
	}

	err := soak.advanceWorkflow(t.Context(), project, project.Workspace.Actors[0], 0)
	if err != nil {
		t.Fatalf("advanceWorkflow() error = %v", err)
	}
	if fake.updatedStateID != projectAStateID {
		t.Fatalf("updated state = %q, want %q", fake.updatedStateID, projectAStateID)
	}
}

type workflowValidationState struct {
	projectID string
	name      string
}

type workflowValidationMCP struct {
	issueProjects  map[string]string
	states         map[string]workflowValidationState
	updatedStateID string
}

func (f *workflowValidationMCP) ServeHTTP(
	writer http.ResponseWriter,
	request *http.Request,
) {
	var payload rpcRequest
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	if payload.Params.Name != "tack_update_issue" {
		http.Error(writer, "unexpected tool "+payload.Params.Name, http.StatusBadRequest)
		return
	}
	var stateReference string
	if err := json.Unmarshal(
		payload.Params.Arguments.Properties["state_id"],
		&stateReference,
	); err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	issueProjectID := f.issueProjects[payload.Params.Arguments.NodeID]
	for stateID, state := range f.states {
		if state.projectID == issueProjectID && state.name == stateReference {
			f.updatedStateID = stateID
			writeRerunResult(writer, "updated", false)
			return
		}
	}
	writeRerunResult(
		writer,
		fmt.Sprintf("properties.state_id %q: invalid argument", stateReference),
		true,
	)
}

func TestWorkflowVisitsEveryIssueInEveryProject(t *testing.T) {
	t.Parallel()
	const projectCount = 8
	const issueCount = 16
	content, err := NewContent(t.Context(), 245)
	if err != nil {
		t.Fatalf("NewContent() error = %v", err)
	}
	soak := &Soak{driver: NewDriver(nil, true, 245), content: content}
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
