package datagen

import (
	"context"
	"fmt"
)

// generateNestedIssue creates one issue with the raw ID of parentIssue, an
// issue of the same project, as its parent_id. No NodeType metadata declares
// an issue under an issue. tack_get_issue must return the nested issue, and
// tack_remove_relationship must refuse the removal of its only child_of edge.
// A project without a generated issue gets no nested issue.
func (g *Generator) generateNestedIssue(
	ctx context.Context,
	workspace WorkspaceIdentity,
	projectIdentifier string,
	existing Result,
	parentIssue string,
) error {
	if parentIssue == "" {
		return nil
	}
	actor := workspace.Actors[0]
	name := fmt.Sprintf("QA nested issue seed %d", g.seed)
	properties := newProperties()
	properties.setString("parent_id", parentIssue)
	nested, created, err := g.ensureNode(ctx, actor.Token, existing, "tack_create_issue", name, ToolArguments{
		WorkspaceReference: workspace.Slug,
		ProjectReference:   projectIdentifier,
		Name:               name,
		Properties:         properties,
	})
	if err != nil {
		return err
	}
	g.recordEnsure(created)
	if _, err := g.driver.Call(ctx, actor.Token, "tack_get_issue", ToolArguments{
		WorkspaceReference: workspace.Slug,
		NodeID:             nested,
	}); err != nil {
		return loggedError(ctx, "qa datagen: get nested issue "+nested, err)
	}
	return g.probeParentRemovalRefused(ctx, actor.Token, nested, parentIssue)
}
