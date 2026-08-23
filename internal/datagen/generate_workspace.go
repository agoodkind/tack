package datagen

import (
	"context"
	"fmt"
)

func (g *Generator) generateWorkspace(
	ctx context.Context,
	workspaceIndex int,
	workspace WorkspaceIdentity,
) error {
	actor := workspace.Actors[workspaceIndex%len(workspace.Actors)]
	if err := g.exerciseWorkspaceReads(ctx, actor, workspace); err != nil {
		return err
	}
	labels, err := g.generateLabels(ctx, actor, workspace, workspaceIndex)
	if err != nil {
		return err
	}
	listArgs := ToolArguments{WorkspaceReference: workspace.Slug}
	existing, err := g.driver.Call(ctx, actor.Token, "tack_list_projects", listArgs)
	if err != nil {
		return loggedError(ctx, "qa datagen: list projects", err)
	}
	for projectIndex := range g.scale.ProjectsPerWorkspace {
		globalIndex := workspaceIndex*g.scale.ProjectsPerWorkspace + projectIndex
		identifier := fmt.Sprintf("Q%02d%02d", workspaceIndex+1, projectIndex+1)
		name := fmt.Sprintf("QA Project %d.%d seed %d", workspaceIndex+1, projectIndex+1, g.seed)
		properties := newProperties()
		properties.setString("identifier", identifier)
		properties.setString("slug", slugify(name))
		properties.setString("description", g.content.Paragraph())
		createArgs := ToolArguments{
			WorkspaceReference: workspace.Slug,
			Name:               name,
			Properties:         properties,
		}
		projectReference, created, err := g.ensureNode(
			ctx,
			actor.Token,
			existing,
			"tack_create_project",
			name,
			createArgs,
		)
		if err != nil {
			return err
		}
		g.recordEnsure(created)
		g.summary.Projects++
		if g.probeNodeID == "" {
			g.probeNodeID = projectReference
		}
		if err := g.generateProject(
			ctx,
			globalIndex,
			workspace,
			identifier,
			projectReference,
			labels,
		); err != nil {
			return err
		}
	}
	return nil
}

func (g *Generator) exerciseWorkspaceReads(
	ctx context.Context,
	actor Actor,
	workspace WorkspaceIdentity,
) error {
	calls := []struct {
		name string
		args ToolArguments
	}{
		{name: "tack_list_workspaces", args: ToolArguments{}},
		{name: "tack_describe_workspace", args: ToolArguments{WorkspaceReference: workspace.Slug}},
		{name: "tack_list_members", args: ToolArguments{WorkspaceReference: workspace.Slug}},
		{name: "tack_list_property_defs", args: ToolArguments{WorkspaceReference: workspace.Slug}},
	}
	for _, call := range calls {
		if _, err := g.driver.Call(ctx, actor.Token, call.name, call.args); err != nil {
			return loggedError(ctx, "qa datagen: "+call.name, err)
		}
	}
	return nil
}

func (g *Generator) ensureNode(
	ctx context.Context,
	token string,
	existing Result,
	toolName string,
	name string,
	args ToolArguments,
) (string, bool, error) {
	created := existing.ReferenceForName(name) == ""
	result, err := g.driver.Call(ctx, token, toolName, args)
	if err != nil {
		return "", false, loggedError(
			ctx,
			fmt.Sprintf("qa datagen: %s %q", toolName, name),
			err,
		)
	}
	reference := result.RawID()
	if reference == "" {
		return "", false, fmt.Errorf("qa datagen: %s %q returned no raw id", toolName, name)
	}
	return reference, created, nil
}

func (g *Generator) recordEnsure(created bool) {
	if created {
		g.summary.Created++
		return
	}
	g.summary.Reused++
}
