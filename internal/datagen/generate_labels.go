package datagen

import (
	"context"
	"fmt"
)

func (g *Generator) generateLabels(
	ctx context.Context,
	actor Actor,
	workspace WorkspaceIdentity,
	workspaceIndex int,
) ([]string, error) {
	listArgs := ToolArguments{WorkspaceReference: workspace.Slug}
	existing, err := g.driver.Call(ctx, actor.Token, "tack_list_labels", listArgs)
	if err != nil {
		return nil, loggedError(ctx, "qa datagen: list labels", err)
	}
	references := make([]string, 0, g.scale.LabelsPerWorkspace)
	for labelIndex := range g.scale.LabelsPerWorkspace {
		name, properties := labelDefinition(
			g.content, g.seed, workspaceIndex, labelIndex,
		)
		reference, created, err := g.ensureNode(
			ctx,
			actor.Token,
			existing,
			"tack_create_label",
			name,
			ToolArguments{
				WorkspaceReference: workspace.Slug,
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
	return references, nil
}

func labelDefinition(
	content *Content,
	seed int64,
	workspaceIndex int,
	labelIndex int,
) (string, NodeProperties) {
	name := fmt.Sprintf(
		"%s-w%02d-seed-%d",
		content.Label(labelIndex),
		workspaceIndex+1,
		seed,
	)
	return name, labelProperties(labelIndex)
}

func labelProperties(labelIndex int) NodeProperties {
	properties := newProperties()
	properties.setString(
		"color",
		fmt.Sprintf("#%06X", (labelIndex+1)*0x1F2A3B&0xFFFFFF),
	)
	properties.setString(
		"qa_select",
		[]string{"planned", "active", "verified"}[labelIndex%3],
	)
	return properties
}
