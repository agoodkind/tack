package datagen

import (
	"context"
	"fmt"
)

func (s *Soak) churnLabel(
	ctx context.Context,
	project *soakProject,
	actor Actor,
	operationIndex int,
	create bool,
) error {
	if !create && len(project.Labels) > 0 {
		return s.updateSimpleNode(
			ctx, project, actor, "label", project.Labels, operationIndex,
		)
	}
	properties := soakProperties(s.content, operationIndex)
	properties.setString("color", fmt.Sprintf("#%06X", operationIndex&0xFFFFFF))
	result, err := s.driver.Call(ctx, actor.Token, "tack_create_label", ToolArguments{
		WorkspaceReference: project.Workspace.Slug,
		Name: fmt.Sprintf(
			"Soak label op-%08d seed-%d",
			operationIndex,
			s.options.Seed,
		),
		Properties: properties,
	})
	if err != nil {
		return loggedError(ctx, "qa datagen soak: create label", err)
	}
	rawID := result.RawID()
	if rawID == "" {
		return fmt.Errorf("qa datagen soak: create label returned no raw id")
	}
	project.Labels = append(project.Labels, soakNode{Reference: rawID, RawID: rawID})
	s.summary.Created++
	return nil
}

func (s *Soak) churnState(
	ctx context.Context,
	project *soakProject,
	actor Actor,
	operationIndex int,
	create bool,
) error {
	if !create && len(project.States) > 0 {
		return s.updateSimpleNode(
			ctx, project, actor, "state", project.States, operationIndex,
		)
	}
	properties := soakProperties(s.content, operationIndex)
	properties.setString("group", "started")
	properties.setInt("sort_order", len(project.States)+10)
	properties.setString("color", fmt.Sprintf("#%06X", operationIndex&0xFFFFFF))
	name := fmt.Sprintf(
		"Soak state op-%08d seed-%d",
		operationIndex,
		s.options.Seed,
	)
	result, err := s.driver.Call(ctx, actor.Token, "tack_create_state", ToolArguments{
		WorkspaceReference: project.Workspace.Slug,
		ProjectReference:   project.Reference,
		Name:               name,
		Properties:         properties,
	})
	if err != nil {
		return loggedError(ctx, "qa datagen soak: create state", err)
	}
	rawID := result.RawID()
	if rawID == "" {
		return fmt.Errorf("qa datagen soak: create state returned no raw id")
	}
	project.States = append(project.States, soakNode{
		Reference: rawID,
		RawID:     rawID,
		Name:      name,
		Group:     "started",
		SortOrder: len(project.States) + 10,
	})
	s.summary.Created++
	return nil
}

func (s *Soak) updateSimpleNode(
	ctx context.Context,
	project *soakProject,
	actor Actor,
	nodeType string,
	nodes []soakNode,
	operationIndex int,
) error {
	target := nodes[operationIndex/soakOperationKinds%len(nodes)]
	_, err := s.driver.Call(
		ctx,
		actor.Token,
		"tack_update_"+nodeType,
		ToolArguments{
			WorkspaceReference: project.Workspace.Slug,
			NodeID:             target.RawID,
			Properties:         soakProperties(s.content, operationIndex),
		},
	)
	if err != nil {
		return loggedError(
			ctx,
			fmt.Sprintf("qa datagen soak: update %s %s", nodeType, target.RawID),
			err,
		)
	}
	s.summary.Updated++
	return nil
}

func (s *Soak) churnProject(
	ctx context.Context,
	project *soakProject,
	actor Actor,
	operationIndex int,
	create bool,
) error {
	if !create {
		return s.updateSimpleNode(
			ctx, project, actor, "project",
			[]soakNode{{RawID: project.RawID}},
			operationIndex,
		)
	}
	return s.createProject(ctx, project.Workspace, actor, operationIndex)
}

func (s *Soak) createProject(
	ctx context.Context,
	workspace WorkspaceIdentity,
	actor Actor,
	operationIndex int,
) error {
	identifier := fmt.Sprintf(
		"S%06X%06X",
		uint64(s.options.Seed)&0xFFFFFF,
		operationIndex&0xFFFFFF,
	)
	properties := soakProperties(s.content, operationIndex)
	properties.setString("identifier", identifier)
	properties.setString("slug", slugify(identifier))
	result, err := s.driver.Call(ctx, actor.Token, "tack_create_project", ToolArguments{
		WorkspaceReference: workspace.Slug,
		Name: fmt.Sprintf(
			"Soak Project op-%08d seed-%d",
			operationIndex,
			s.options.Seed,
		),
		Properties: properties,
	})
	if err != nil {
		return loggedError(ctx, "qa datagen soak: create project", err)
	}
	rawID := result.RawID()
	if rawID == "" {
		return fmt.Errorf("qa datagen soak: create project returned no raw id")
	}
	s.summary.Created++
	return nil
}
