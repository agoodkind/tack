package datagen

import (
	"context"
)

// deleteResidualCascadeProject deletes the probe project named name when an
// interrupted earlier run left it in the workspace.
func (g *Generator) deleteResidualCascadeProject(
	ctx context.Context,
	token string,
	workspaceReference string,
	name string,
	proof *CascadeProbeResult,
) error {
	existing, err := g.driver.Call(
		ctx,
		token,
		"tack_list_projects",
		scopeArgs(workspaceReference, ""),
	)
	if err != nil {
		return loggedError(ctx, "qa datagen: list projects for the cascade probe", err)
	}
	residual := existing.ReferenceForName(name)
	if residual == "" {
		return nil
	}
	deleted, err := g.driver.Call(
		ctx,
		token,
		"tack_delete_project",
		ToolArguments{
			WorkspaceReference: workspaceReference,
			NodeID:             residual,
		},
	)
	if err != nil {
		return loggedError(ctx, "qa datagen: delete residual cascade probe project "+residual, err)
	}
	if g.dryRun {
		return nil
	}
	if err := recordCascadeDelete(ctx, proof, deleted, residual); err != nil {
		return err
	}
	return g.requireCascadeGone(
		ctx,
		token,
		workspaceReference,
		residual,
		deleted,
		residual,
		nil,
		"",
	)
}
