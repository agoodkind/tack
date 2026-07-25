package datagen

import (
	"context"
	"fmt"
)

func (g *Generator) generateProject(
	ctx context.Context,
	projectIndex int,
	workspace WorkspaceIdentity,
	projectIdentifier string,
	projectReference string,
	labels []string,
) error {
	actor := workspace.Actors[projectIndex%len(workspace.Actors)]
	if err := g.generateStates(ctx, actor, workspace, projectIdentifier); err != nil {
		return err
	}
	containers, err := g.generateContainers(
		ctx,
		actor,
		workspace,
		projectIdentifier,
	)
	if err != nil {
		return err
	}
	return g.generateIssues(
		ctx,
		projectIndex,
		workspace,
		projectIdentifier,
		projectReference,
		containers,
		labels,
	)
}

func (g *Generator) generateStates(
	ctx context.Context,
	actor Actor,
	workspace WorkspaceIdentity,
	projectIdentifier string,
) error {
	listArgs := scopeArgs(workspace.Slug, projectIdentifier)
	existing, err := g.driver.Call(ctx, actor.Token, "tack_list_states", listArgs)
	if err != nil {
		return loggedError(ctx, "qa datagen: list states", err)
	}
	for stateIndex := range g.scale.AdditionalStateCount {
		name := fmt.Sprintf("%s seed %d", g.content.Workflow(stateIndex), g.seed)
		properties := newProperties()
		properties.setString("group", "started")
		properties.setString(
			"color",
			fmt.Sprintf("#%06X", (stateIndex+3)*0x2A4C6D&0xFFFFFF),
		)
		properties.setInt("sort_order", stateIndex+20)
		_, created, err := g.ensureNode(
			ctx,
			actor.Token,
			existing,
			"tack_create_state",
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
		g.recordEnsure(created)
	}
	return nil
}

func (g *Generator) generateContainers(
	ctx context.Context,
	actor Actor,
	workspace WorkspaceIdentity,
	projectIdentifier string,
) ([]string, error) {
	references := make([]string, 0, g.scale.ContainersPerProject*3)
	for _, nodeType := range []struct {
		singular string
		plural   string
	}{
		{singular: "epic", plural: "epics"},
		{singular: "cycle", plural: "cycles"},
		{singular: "module", plural: "modules"},
	} {
		listArgs := scopeArgs(workspace.Slug, projectIdentifier)
		existing, err := g.driver.Call(
			ctx,
			actor.Token,
			"tack_list_"+nodeType.plural,
			listArgs,
		)
		if err != nil {
			return nil, loggedError(ctx, "qa datagen: list "+nodeType.plural, err)
		}
		for containerIndex := range g.scale.ContainersPerProject {
			name := fmt.Sprintf(
				"QA %s %d seed %d",
				nodeType.singular,
				containerIndex+1,
				g.seed,
			)
			properties := newProperties()
			properties.setString("description", g.content.Paragraph())
			properties.setBool("qa_checkbox", containerIndex%2 == 0)
			reference, created, err := g.ensureNode(
				ctx,
				actor.Token,
				existing,
				"tack_create_"+nodeType.singular,
				name,
				ToolArguments{
					WorkspaceReference: workspace.Slug,
					ProjectReference:   projectIdentifier,
					Name:               name,
					Properties:         properties,
				},
			)
			if err != nil {
				return nil, err
			}
			g.recordEnsure(created)
			references = append(references, reference)
		}
	}
	return references, nil
}

func scopeArgs(workspaceReference, projectReference string) ToolArguments {
	return ToolArguments{
		WorkspaceReference: workspaceReference,
		ProjectReference:   projectReference,
	}
}
