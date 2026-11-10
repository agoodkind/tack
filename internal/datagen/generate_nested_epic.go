package datagen

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// parentCycleRefusalText is part of the error that the node service returns
// when a parent_id value is the node's own ID or the ID of one of its
// descendants.
const parentCycleRefusalText = "one of its descendants"

// errParentCycleAccepted reports that an update stored a parent_id value
// that is the node's own ID or the ID of one of its descendants.
var errParentCycleAccepted = errors.New("the update stored a parent_id that is the node or one of its descendants")

// generateNestedEpic skips a project without a generated epic and otherwise
// creates an epic in the same project with parent_id set to the raw ID of
// parentEpic. It requires tack_get_epic to return the nested epic. It then
// requires tack_update_epic to refuse parentEpic's move under the nested
// epic and the nested epic's move under itself.
func (g *Generator) generateNestedEpic(
	ctx context.Context,
	actor Actor,
	workspace WorkspaceIdentity,
	projectIdentifier string,
	parentEpic string,
) error {
	if parentEpic == "" {
		return nil
	}
	existing, err := g.driver.Call(ctx, actor.Token, "tack_list_epics", scopeArgs(workspace.Slug, projectIdentifier))
	if err != nil {
		return loggedError(ctx, "qa datagen: list epics before the nested epic", err)
	}
	name := fmt.Sprintf("QA nested epic seed %d", g.seed)
	properties := newProperties()
	properties.setString("parent_id", parentEpic)
	nested, created, err := g.ensureNode(ctx, actor.Token, existing, "tack_create_epic", name, ToolArguments{
		WorkspaceReference: workspace.Slug,
		ProjectReference:   projectIdentifier,
		Name:               name,
		Properties:         properties,
	})
	if err != nil {
		return err
	}
	g.recordEnsure(created)
	if _, err := g.driver.Call(ctx, actor.Token, "tack_get_epic", ToolArguments{
		WorkspaceReference: workspace.Slug,
		NodeID:             nested,
	}); err != nil {
		return loggedError(ctx, "qa datagen: get nested epic "+nested, err)
	}
	if err := g.probeParentCycleRefused(ctx, actor.Token, workspace.Slug, parentEpic, nested); err != nil {
		return err
	}
	return g.probeParentCycleRefused(ctx, actor.Token, workspace.Slug, nested, nested)
}

// probeParentCycleRefused calls tack_update_epic to set the parent_id of
// epicReference to parentReference, which is the epic itself or one of its
// descendants. The server must refuse the call. A dry run plans the call and
// skips the check.
func (g *Generator) probeParentCycleRefused(ctx context.Context, token, workspaceReference, epicReference, parentReference string) error {
	properties := newProperties()
	properties.setString("parent_id", parentReference)
	_, err := g.driver.Call(ctx, token, "tack_update_epic", ToolArguments{
		WorkspaceReference: workspaceReference,
		NodeID:             epicReference,
		Properties:         properties,
	})
	if g.dryRun {
		return nil
	}
	message := fmt.Sprintf("qa datagen: parent cycle probe for epic %s and parent %s", epicReference, parentReference)
	if err == nil {
		return loggedError(ctx, message, errParentCycleAccepted)
	}
	if !strings.Contains(err.Error(), parentCycleRefusalText) {
		return loggedError(ctx, message, err)
	}
	slog.InfoContext(ctx, "qa.datagen.parent_cycle_refused",
		slog.String("epic", epicReference), slog.String("parent", parentReference))
	return nil
}
