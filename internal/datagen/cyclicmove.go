package datagen

import (
	"context"
	"fmt"
	"strings"
)

// movedCountText is the field of the tack_delete_cycle output that reports
// one issue moved to the project.
const movedCountText = "Moved nodes: 1"

// probeCycleMove creates a cycle in the probe project and an issue with the
// cycle as its parent, then deletes the cycle through tack_delete_cycle. The
// delete must move the issue to the project: the output must report one
// moved node, and tack_get_issue must still return the issue. It returns
// the issue's raw id. A dry run plans the calls and skips the checks.
func (g *Generator) probeCycleMove(
	ctx context.Context,
	token string,
	workspaceReference string,
	identifier string,
) (string, error) {
	cycle, err := g.driver.Call(
		ctx,
		token,
		"tack_create_cycle",
		ToolArguments{
			WorkspaceReference: workspaceReference,
			ProjectReference:   identifier,
			Name:               "QA cascade delete cycle",
		},
	)
	if err != nil {
		return "", loggedError(ctx, "qa datagen: create cascade probe cycle", err)
	}
	issue, err := g.driver.Call(
		ctx,
		token,
		"tack_create_issue",
		ToolArguments{
			WorkspaceReference: workspaceReference,
			ProjectReference:   identifier,
			Name:               "QA cascade delete moved issue",
		},
	)
	if err != nil {
		return "", loggedError(ctx, "qa datagen: create cascade probe issue in the cycle", err)
	}
	if err := g.callRelationship(
		ctx,
		token,
		"tack_add_relationship",
		issue.RawID(),
		"child_of",
		cycle.RawID(),
	); err != nil {
		return "", err
	}
	deleted, err := g.driver.Call(
		ctx,
		token,
		"tack_delete_cycle",
		ToolArguments{
			WorkspaceReference: workspaceReference,
			NodeID:             cycle.RawID(),
		},
	)
	if err != nil {
		return "", loggedError(ctx, "qa datagen: delete cascade probe cycle", err)
	}
	if g.dryRun {
		return issue.RawID(), nil
	}
	if !strings.Contains(deleted.Text(), movedCountText) {
		return "", loggedError(
			ctx,
			"qa datagen: delete output of cascade probe cycle",
			fmt.Errorf("output has no %q field:\n%s", movedCountText, deleted.Text()),
		)
	}
	if _, err := g.driver.Call(
		ctx,
		token,
		"tack_get_issue",
		ToolArguments{WorkspaceReference: workspaceReference, NodeID: issue.RawID()},
	); err != nil {
		return "", loggedError(ctx, "qa datagen: get the issue the cycle delete moved", err)
	}
	return issue.RawID(), nil
}
