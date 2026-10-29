package datagen

import (
	"context"
	"fmt"
)

// generateEpicScopedIssue creates or reuses a seed issue and lists the epic's issues
// when epicReference is nonempty.
func (g *Generator) generateEpicScopedIssue(
	ctx context.Context,
	actor Actor,
	workspace WorkspaceIdentity,
	projectIdentifier string,
	epicReference string,
) error {
	if epicReference == "" {
		return nil
	}
	existing, err := g.driver.Call(ctx, actor.Token, "tack_list_issues", scopeArgs(workspace.Slug, projectIdentifier))
	if err != nil {
		return loggedError(ctx, "qa datagen: list issues before the epic-scoped issue", err)
	}
	name := fmt.Sprintf("QA epic-scoped issue seed %d", g.seed)
	_, created, err := g.ensureNode(ctx, actor.Token, existing, "tack_create_issue", name, ToolArguments{
		WorkspaceReference: workspace.Slug,
		EpicReference:      epicReference,
		Name:               name,
	})
	if err != nil {
		return err
	}
	g.recordEnsure(created)
	if _, err := g.driver.Call(ctx, actor.Token, "tack_list_issues", ToolArguments{
		WorkspaceReference: workspace.Slug,
		EpicReference:      epicReference,
	}); err != nil {
		return loggedError(ctx, "qa datagen: list issues under epic "+epicReference, err)
	}
	return nil
}
