package datagen

import (
	"context"
)

func (g *Generator) generateIssueDetails(
	ctx context.Context,
	index int,
	actor Actor,
	workspace WorkspaceIdentity,
	projectIdentifier string,
	issueReference string,
	parentReference string,
	labels []string,
) error {
	if err := g.generateRelationships(
		ctx,
		index,
		actor,
		workspace,
		issueReference,
		parentReference,
		labels,
	); err != nil {
		return err
	}
	if err := g.generateCommentsAndActivity(
		ctx,
		index,
		actor,
		workspace,
		projectIdentifier,
		issueReference,
	); err != nil {
		return err
	}
	if index%4 == 0 {
		if err := g.exerciseIssueReads(
			ctx,
			actor,
			workspace,
			projectIdentifier,
			issueReference,
		); err != nil {
			return err
		}
	}
	if index%5 == 0 {
		return g.updateIssueAndCheckConfirmation(ctx, actor, workspace, projectIdentifier, issueReference)
	}
	return nil
}

func (g *Generator) exerciseIssueReads(
	ctx context.Context,
	actor Actor,
	workspace WorkspaceIdentity,
	projectIdentifier string,
	issueReference string,
) error {
	calls := []struct {
		name string
		args ToolArguments
	}{
		{
			name: "tack_get_issue",
			args: ToolArguments{
				WorkspaceReference: workspace.Slug,
				NodeID:             issueReference,
			},
		},
		{
			name: "tack_get_properties",
			args: ToolArguments{NodeID: issueReference},
		},
		{
			name: "tack_list_relationships",
			args: ToolArguments{NodeID: issueReference, Direction: "out"},
		},
	}
	for _, call := range calls {
		if _, err := g.driver.Call(ctx, actor.Token, call.name, call.args); err != nil {
			return loggedError(ctx, "qa datagen: "+call.name, err)
		}
	}
	return nil
}
