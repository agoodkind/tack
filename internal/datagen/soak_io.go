package datagen

import (
	"context"
	"fmt"
)

func (s *Soak) churnRelationship(
	ctx context.Context,
	project *soakProject,
	actor Actor,
	operationIndex int,
) error {
	if len(project.Issues) == 0 {
		return s.churnIssue(ctx, project, actor, operationIndex, true)
	}
	issue := project.Issues[operationIndex/soakOperationKinds%len(project.Issues)]
	target := project.Workspace.Actors[(operationIndex+1)%len(project.Workspace.Actors)].UserID.String()
	toolName := "tack_add_relationship"
	if operationIndex%4 == 0 {
		toolName = "tack_remove_relationship"
	}
	return s.callRelationship(
		ctx,
		actor.Token,
		toolName,
		issue.RawID,
		"watches",
		target,
	)
}

func (s *Soak) relationship(
	ctx context.Context,
	token string,
	sourceID string,
	relationType string,
	targetID string,
) error {
	return s.callRelationship(
		ctx,
		token,
		"tack_add_relationship",
		sourceID,
		relationType,
		targetID,
	)
}

func (s *Soak) callRelationship(
	ctx context.Context,
	token string,
	toolName string,
	sourceID string,
	relationType string,
	targetID string,
) error {
	_, err := s.driver.Call(ctx, token, toolName, ToolArguments{
		SourceID:     sourceID,
		RelationType: relationType,
		TargetID:     targetID,
	})
	if err != nil {
		return loggedError(
			ctx,
			fmt.Sprintf(
				"qa datagen soak: %s %s %s %s",
				toolName,
				sourceID,
				relationType,
				targetID,
			),
			err,
		)
	}
	s.summary.Relationships++
	return nil
}

func (s *Soak) readProject(
	ctx context.Context,
	project *soakProject,
	actor Actor,
	operationIndex int,
) error {
	var toolName string
	var args ToolArguments
	switch operationIndex / soakOperationKinds % 3 {
	case 0:
		toolName = "tack_list_issues"
		args = scopeArgs(project.Workspace.Slug, project.Reference)
	case 1:
		toolName = "tack_get_project"
		args = ToolArguments{
			WorkspaceReference: project.Workspace.Slug,
			NodeID:             project.RawID,
		}
	default:
		toolName = "tack_search"
		args = ToolArguments{
			WorkspaceReference: project.Workspace.Slug,
			ProjectReference:   project.Reference,
			Query:              "soak",
			NodeType:           "issue",
		}
	}
	if _, err := s.driver.Call(ctx, actor.Token, toolName, args); err != nil {
		loggedDiscoveryListSkip(ctx, "project", project.RawID, err)
		return nil
	}
	s.summary.Reads++
	return nil
}
