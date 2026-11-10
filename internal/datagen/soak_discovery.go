package datagen

import (
	"context"
)

func discoverSoakProjects(
	ctx context.Context,
	session *runSession,
) ([]*soakProject, bool, error) {
	var projects []*soakProject
	complete := true
	for workspaceIndex, workspace := range session.identities.Workspaces {
		workspaceProjects, err := discoverWorkspaceProjects(
			ctx, session, workspace, workspaceIndex,
		)
		if err != nil {
			return nil, false, err
		}
		if len(workspaceProjects) == 0 {
			complete = false
		}
		projects = append(projects, workspaceProjects...)
	}
	return projects, complete, nil
}

func discoverWorkspaceProjects(
	ctx context.Context,
	session *runSession,
	workspace WorkspaceIdentity,
	workspaceIndex int,
) ([]*soakProject, error) {
	actor := workspace.Actors[0]
	projectList, err := session.driver.Call(
		ctx,
		actor.Token,
		"tack_list_projects",
		ToolArguments{WorkspaceReference: workspace.Slug},
	)
	if err != nil {
		loggedDiscoveryListSkip(ctx, "project", workspace.Slug, err)
		return nil, nil
	}
	labels, err := discoverLabels(ctx, session, workspace, workspaceIndex)
	if err != nil {
		return nil, err
	}
	var projects []*soakProject
	for projectOffset, item := range projectList.collectionItems() {
		definition := seededProjectDefinition(
			session, workspace, workspaceIndex, projectOffset,
		)
		projectIndex := workspaceIndex*session.scale.ProjectsPerWorkspace + projectOffset
		project, err := discoverProject(
			ctx, session, workspace, item, labels, projectIndex, &definition,
		)
		if err != nil {
			return nil, err
		}
		if project == nil {
			continue
		}
		if len(project.States) == 0 || len(project.Issues) == 0 {
			logIncompleteProjectSkip(ctx, item, len(project.States), len(project.Issues))
			continue
		}
		projects = append(projects, project)
	}
	return projects, nil
}

func discoverProject(
	ctx context.Context,
	session *runSession,
	workspace WorkspaceIdentity,
	item collectionItem,
	labels []soakNode,
	projectIndex int,
	recovery *discoveryDefinition,
) (*soakProject, error) {
	actor := workspace.Actors[0]
	projectNode, err := resolveCollectionItem(
		ctx,
		session.driver,
		actor.Token,
		workspace.Slug,
		"project",
		item,
		workspace.Slug,
		recovery,
	)
	if err != nil {
		return nil, nil
	}
	project := &soakProject{
		Workspace: workspace,
		Reference: projectNode.RawID,
		RawID:     projectNode.RawID,
		Labels:    append([]soakNode(nil), labels...),
	}
	projectReference := project.RawID
	resolutionScope := item.Reference
	stateDefinitions := seededStateDefinitions(
		session, workspace, projectIndex, projectReference,
	)
	project.States, err = discoverScopedNodes(
		ctx, session.driver, actor.Token, workspace.Slug, projectReference, resolutionScope,
		"state", "states", stateDefinitions,
	)
	if err != nil {
		return nil, err
	}
	containerDefinitions := seededContainerDefinitions(
		session, workspace, projectIndex, projectReference,
	)
	project.Epics, err = discoverScopedNodes(
		ctx, session.driver, actor.Token, workspace.Slug, projectReference, resolutionScope,
		"epic", "epics", containerDefinitions["epic"],
	)
	if err != nil {
		return nil, err
	}
	project.Cycles, err = discoverScopedNodes(
		ctx, session.driver, actor.Token, workspace.Slug, projectReference, resolutionScope,
		"cycle", "cycles", containerDefinitions["cycle"],
	)
	if err != nil {
		return nil, err
	}
	project.Modules, err = discoverScopedNodes(
		ctx, session.driver, actor.Token, workspace.Slug, projectReference, resolutionScope,
		"module", "modules", containerDefinitions["module"],
	)
	if err != nil {
		return nil, err
	}
	containers := append(append([]soakNode{}, project.Epics...), project.Cycles...)
	containers = append(containers, project.Modules...)
	issueDefinitions := seededIssueDefinitions(
		session,
		workspace,
		projectIndex,
		projectReference,
		project.RawID,
		containers,
	)
	issues, err := discoverScopedNodes(
		ctx, session.driver, actor.Token, workspace.Slug, projectReference, resolutionScope,
		"issue", "issues", issueDefinitions,
	)
	if err != nil {
		return nil, err
	}
	for index, issue := range issues {
		project.Issues = append(project.Issues, &soakIssue{
			soakNode: issue,
			Reopen:   index%5 == 0,
		})
	}
	return project, nil
}
