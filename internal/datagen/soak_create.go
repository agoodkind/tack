package datagen

import (
	"context"
	"fmt"
)

func (s *Soak) churnIssue(
	ctx context.Context,
	project *soakProject,
	actor Actor,
	operationIndex int,
	create bool,
) error {
	if !create && len(project.Issues) > 0 {
		return s.updateIssue(ctx, project, actor, operationIndex)
	}
	issue, err := s.createIssue(ctx, project, actor, operationIndex)
	if err != nil {
		return err
	}
	project.Issues = append(project.Issues, issue)
	return nil
}

func (s *Soak) createIssue(
	ctx context.Context,
	project *soakProject,
	actor Actor,
	operationIndex int,
) (*soakIssue, error) {
	name := fmt.Sprintf(
		"Soak %s op-%08d seed-%d",
		s.content.IssueTitle(operationIndex),
		operationIndex,
		s.options.Seed,
	)
	properties := issueProperties(
		s.content,
		actor,
		operationIndex,
		s.content.ReferenceTime(),
	)
	name = InjectEdgeCases(
		operationIndex,
		name,
		properties,
		s.content.ReferenceTime(),
	)
	result, err := s.driver.Call(ctx, actor.Token, "tack_create_issue", ToolArguments{
		WorkspaceReference: project.Workspace.Slug,
		ProjectReference:   project.Reference,
		Name:               name,
		Properties:         properties,
	})
	if err != nil {
		return nil, loggedError(ctx, "qa datagen soak: create issue", err)
	}
	rawID := result.RawID()
	if rawID == "" {
		return nil, fmt.Errorf("qa datagen soak: create issue returned no raw id")
	}
	s.summary.Created++
	return &soakIssue{
		soakNode: soakNode{Reference: rawID, RawID: rawID, Name: name},
		Reopen:   operationIndex%5 == 0,
	}, nil
}

func (s *Soak) createIssueChildForProject(
	ctx context.Context,
	project *soakProject,
	actor Actor,
	nodeType string,
	operationIndex int,
) error {
	if len(project.Issues) == 0 {
		return s.churnIssue(ctx, project, actor, operationIndex, true)
	}
	issue := project.Issues[operationIndex/soakOperationKinds%len(project.Issues)]
	return s.createIssueChild(ctx, project, actor, issue, nodeType, operationIndex)
}

func (s *Soak) createIssueChild(
	ctx context.Context,
	project *soakProject,
	actor Actor,
	issue *soakIssue,
	nodeType string,
	operationIndex int,
) error {
	properties := soakProperties(s.content, operationIndex)
	result, err := s.driver.Call(
		ctx,
		actor.Token,
		"tack_create_"+nodeType,
		ToolArguments{
			WorkspaceReference: project.Workspace.Slug,
			ProjectReference:   project.Reference,
			IssueReference:     issue.RawID,
			Name: fmt.Sprintf(
				"Soak %s op-%08d seed-%d",
				nodeType,
				operationIndex,
				s.options.Seed,
			),
			Properties: properties,
		},
	)
	if err != nil {
		return loggedError(ctx, "qa datagen soak: create "+nodeType, err)
	}
	if result.RawID() == "" {
		return fmt.Errorf("qa datagen soak: create %s returned no raw id", nodeType)
	}
	s.summary.Created++
	if nodeType == "comment" {
		s.summary.Comments++
	}
	return nil
}

func (s *Soak) churnContainer(
	ctx context.Context,
	project *soakProject,
	actor Actor,
	nodeType soakNodeType,
	nodes *[]soakNode,
	operationIndex int,
	create bool,
) error {
	if !create && len(*nodes) > 0 {
		return s.updateContainer(
			ctx, project, actor, nodeType, *nodes, operationIndex,
		)
	}
	properties := soakProperties(s.content, operationIndex)
	result, err := s.driver.Call(
		ctx,
		actor.Token,
		"tack_create_"+string(nodeType),
		ToolArguments{
			WorkspaceReference: project.Workspace.Slug,
			ProjectReference:   project.Reference,
			Name: fmt.Sprintf(
				"Soak %s op-%08d seed-%d",
				nodeType,
				operationIndex,
				s.options.Seed,
			),
			Properties: properties,
		},
	)
	if err != nil {
		return loggedError(ctx, "qa datagen soak: create "+string(nodeType), err)
	}
	rawID := result.RawID()
	if rawID == "" {
		return fmt.Errorf("qa datagen soak: create %s returned no raw id", nodeType)
	}
	*nodes = append(*nodes, soakNode{Reference: rawID, RawID: rawID})
	s.summary.Created++
	return nil
}
