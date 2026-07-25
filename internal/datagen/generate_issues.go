package datagen

import (
	"context"
	"fmt"
)

func (g *Generator) generateIssues(
	ctx context.Context,
	projectIndex int,
	workspace WorkspaceIdentity,
	projectIdentifier string,
	projectReference string,
	containers []string,
	labels []string,
) error {
	listArgs := scopeArgs(workspace.Slug, projectIdentifier)
	existing, err := g.driver.Call(ctx, workspace.Actors[0].Token, "tack_list_issues", listArgs)
	if err != nil {
		return loggedError(ctx, "qa datagen: list issues", err)
	}
	createdIssue := false
	for issueIndex := range g.scale.IssuesPerProject {
		globalIndex := projectIndex*g.scale.IssuesPerProject + issueIndex
		actor := workspace.Actors[globalIndex%len(workspace.Actors)]
		name := g.content.IssueTitle(globalIndex)
		properties := issueProperties(g.content, actor, globalIndex, g.now())
		name = InjectEdgeCases(globalIndex, name, properties, g.now())
		parentReference := projectReference
		if len(containers) > 0 && issueIndex%3 != 0 {
			parentReference = containers[issueIndex%len(containers)]
			properties.setString("parent_id", parentReference)
		}
		issueReference, created, err := g.ensureNode(
			ctx,
			actor.Token,
			existing,
			"tack_create_issue",
			name,
			ToolArguments{
				WorkspaceReference: workspace.Slug,
				ProjectReference:   projectIdentifier,
				Name:               name,
				Properties:         properties,
			},
		)
		if err != nil {
			return err
		}
		if created {
			createdIssue = true
		}
		g.recordEnsure(created)
		g.summary.Issues++
		if err := g.generateIssueDetails(
			ctx,
			globalIndex,
			actor,
			workspace,
			projectIdentifier,
			issueReference,
			parentReference,
			labels,
		); err != nil {
			return err
		}
	}
	return g.generateDeletedIssue(
		ctx,
		workspace,
		projectIdentifier,
		existing,
		createdIssue,
	)
}

func (g *Generator) generateDeletedIssue(
	ctx context.Context,
	workspace WorkspaceIdentity,
	projectIdentifier string,
	existing Result,
	createProbe bool,
) error {
	actor := workspace.Actors[0]
	name := fmt.Sprintf("QA deletion probe seed %d", g.seed)
	existingReference := existing.ReferenceForName(name)
	if existingReference != "" {
		result, err := g.driver.Call(ctx, actor.Token, "tack_get_issue", ToolArguments{
			WorkspaceReference: workspace.Slug,
			NodeID:             existingReference,
		})
		if err != nil {
			return loggedError(ctx, "qa datagen: get residual deletion probe", err)
		}
		return g.deleteIssueProbe(ctx, actor, workspace.Slug, result.RawID())
	}
	if !createProbe {
		return nil
	}
	properties := newProperties()
	properties.setString(
		"description",
		"This node is deleted immediately to exercise the real delete path.",
	)
	result, err := g.driver.Call(
		ctx,
		actor.Token,
		"tack_create_issue",
		ToolArguments{
			WorkspaceReference: workspace.Slug,
			ProjectReference:   projectIdentifier,
			Name:               name,
			Properties:         properties,
		},
	)
	if err != nil {
		return loggedError(ctx, "qa datagen: create deletion probe", err)
	}
	return g.deleteIssueProbe(ctx, actor, workspace.Slug, result.RawID())
}

func (g *Generator) deleteIssueProbe(
	ctx context.Context,
	actor Actor,
	workspaceReference string,
	nodeID string,
) error {
	if nodeID == "" {
		return fmt.Errorf("qa datagen: deletion probe returned no raw id")
	}
	_, err := g.driver.Call(ctx, actor.Token, "tack_delete_issue", ToolArguments{
		WorkspaceReference: workspaceReference,
		NodeID:             nodeID,
	})
	if err != nil {
		return loggedError(ctx, "qa datagen: delete probe", err)
	}
	return nil
}
