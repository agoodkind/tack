package datagen

import (
	"context"
	"fmt"
)

func (g *Generator) generateCommentsAndActivity(
	ctx context.Context,
	issueIndex int,
	actor Actor,
	workspace WorkspaceIdentity,
	projectIdentifier string,
	issueReference string,
) error {
	if err := g.generateChildType(
		ctx,
		actor,
		workspace,
		projectIdentifier,
		issueReference,
		issueIndex,
		"comment",
		"comments",
		g.scale.CommentsPerIssue,
	); err != nil {
		return err
	}
	return g.generateChildType(
		ctx,
		actor,
		workspace,
		projectIdentifier,
		issueReference,
		issueIndex,
		"activity",
		"activities",
		g.scale.ActivitiesPerIssue,
	)
}

func (g *Generator) generateChildType(
	ctx context.Context,
	actor Actor,
	workspace WorkspaceIdentity,
	projectIdentifier string,
	issueReference string,
	issueIndex int,
	singular string,
	plural string,
	count int,
) error {
	scope := ToolArguments{
		WorkspaceReference: workspace.Slug,
		ProjectReference:   projectIdentifier,
		IssueReference:     issueReference,
	}
	existing, err := g.driver.Call(ctx, actor.Token, "tack_list_"+plural, scope)
	if err != nil {
		return loggedError(ctx, "qa datagen: list "+plural, err)
	}
	for childIndex := range count {
		name := fmt.Sprintf(
			"QA %s issue-%03d item-%02d by %s seed-%d",
			singular,
			issueIndex+1,
			childIndex+1,
			g.content.Name(),
			g.seed,
		)
		properties := newProperties()
		properties.setString("description", g.content.Comment())
		properties.setString("qa_text", g.content.Sentence())
		args := ToolArguments{
			WorkspaceReference: workspace.Slug,
			ProjectReference:   projectIdentifier,
			IssueReference:     issueReference,
			Name:               name,
			Properties:         properties,
		}
		_, created, err := g.ensureNode(
			ctx,
			actor.Token,
			existing,
			"tack_create_"+singular,
			name,
			args,
		)
		if err != nil {
			return err
		}
		g.recordEnsure(created)
	}
	return nil
}
