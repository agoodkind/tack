package datagen

import (
	"context"
	"fmt"
)

type workflowActionKind string

const (
	workflowSetState workflowActionKind = "set_state"
	workflowAssign   workflowActionKind = "assign"
	workflowLabel    workflowActionKind = "label"
	workflowComment  workflowActionKind = "comment"
)

type workflowAction struct {
	Kind       workflowActionKind
	StateIndex int
}

func nextWorkflowAction(issue *soakIssue, stateCount int) workflowAction {
	switch {
	case issue.Stage == 0:
		issue.Stage++
		return workflowAction{Kind: workflowSetState, StateIndex: 0}
	case issue.Stage == 1:
		issue.Stage++
		return workflowAction{Kind: workflowAssign}
	case issue.Stage == 2:
		issue.Stage++
		return workflowAction{Kind: workflowLabel}
	case issue.Stage == 3:
		issue.Stage++
		return workflowAction{Kind: workflowComment}
	case issue.Stage < stateCount+3:
		stateIndex := issue.Stage - 3
		issue.Stage++
		return workflowAction{Kind: workflowSetState, StateIndex: stateIndex}
	case issue.Reopen:
		issue.Stage = 1
		return workflowAction{Kind: workflowSetState, StateIndex: 0}
	default:
		return workflowAction{Kind: workflowComment}
	}
}

func (s *Soak) advanceWorkflow(
	ctx context.Context,
	project *soakProject,
	actor Actor,
	operationIndex int,
) error {
	states := project.workflowStates()
	if len(states) == 0 || len(project.Issues) == 0 {
		return nil
	}
	workflowIndex := operationIndex / soakOperationKinds
	issue := project.Issues[project.workflowIssueCursor%len(project.Issues)]
	project.workflowIssueCursor++
	action := nextWorkflowAction(issue, len(states))
	switch action.Kind {
	case workflowSetState:
		return s.setIssueState(ctx, project, actor, issue, states[action.StateIndex])
	case workflowAssign:
		return s.relationship(
			ctx, actor.Token, issue.RawID, "assigned_to", actor.UserID.String(),
		)
	case workflowLabel:
		if len(project.Labels) == 0 {
			return nil
		}
		label := project.Labels[workflowIndex%len(project.Labels)]
		return s.relationship(
			ctx, actor.Token, issue.RawID, "labeled_with", label.RawID,
		)
	case workflowComment:
		return s.createIssueChild(ctx, project, actor, issue, "comment", operationIndex)
	default:
		return fmt.Errorf("qa datagen soak: unknown workflow action %q", action.Kind)
	}
}

func (s *Soak) setIssueState(
	ctx context.Context,
	project *soakProject,
	actor Actor,
	issue *soakIssue,
	state soakNode,
) error {
	properties := newProperties()
	properties.setString("state_id", state.Name)
	_, err := s.driver.Call(ctx, actor.Token, "tack_update_issue", ToolArguments{
		WorkspaceReference: project.Workspace.Slug,
		NodeID:             issue.RawID,
		Properties:         properties,
	})
	if err != nil {
		return loggedError(ctx, "qa datagen soak: update issue state "+issue.RawID, err)
	}
	s.summary.Updated++
	return nil
}
