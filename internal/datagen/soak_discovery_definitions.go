package datagen

import "fmt"

func seededProjectDefinition(
	session *runSession,
	workspace WorkspaceIdentity,
	workspaceIndex int,
	projectIndex int,
) discoveryDefinition {
	identifier := fmt.Sprintf("Q%02d%02d", workspaceIndex+1, projectIndex+1)
	name := fmt.Sprintf(
		"QA Project %d.%d seed %d",
		workspaceIndex+1,
		projectIndex+1,
		session.seed,
	)
	properties := newProperties()
	properties.setString("identifier", identifier)
	properties.setString("slug", slugify(name))
	properties.setString("description", session.content.Paragraph())
	actor := workspace.Actors[workspaceIndex%len(workspace.Actors)]
	return discoveryDefinition{
		token: actor.Token,
		arguments: ToolArguments{
			WorkspaceReference: workspace.Slug,
			Name:               name,
			Properties:         properties,
		},
		reference: identifier,
	}
}

func seededStateDefinitions(
	session *runSession,
	workspace WorkspaceIdentity,
	projectIndex int,
	projectReference string,
) map[string]discoveryDefinition {
	definitions := make(map[string]discoveryDefinition, session.scale.AdditionalStateCount)
	actor := workspace.Actors[projectIndex%len(workspace.Actors)]
	for stateIndex := range session.scale.AdditionalStateCount {
		name := fmt.Sprintf("%s seed %d", session.content.Workflow(stateIndex), session.seed)
		properties := newProperties()
		properties.setString("group", "started")
		properties.setString(
			"color",
			fmt.Sprintf("#%06X", (stateIndex+3)*0x2A4C6D&0xFFFFFF),
		)
		properties.setInt("sort_order", stateIndex+20)
		definitions[name] = discoveryDefinition{
			token: actor.Token,
			arguments: ToolArguments{
				WorkspaceReference: workspace.Slug,
				ProjectReference:   projectReference,
				Name:               name,
				Properties:         properties,
			},
		}
	}
	return definitions
}

func seededContainerDefinitions(
	session *runSession,
	workspace WorkspaceIdentity,
	projectIndex int,
	projectReference string,
) map[string]map[string]discoveryDefinition {
	definitions := make(map[string]map[string]discoveryDefinition, 3)
	actor := workspace.Actors[projectIndex%len(workspace.Actors)]
	for _, singular := range []string{"epic", "cycle", "module"} {
		byName := make(map[string]discoveryDefinition, session.scale.ContainersPerProject)
		for containerIndex := range session.scale.ContainersPerProject {
			name := fmt.Sprintf(
				"QA %s %d seed %d",
				singular,
				containerIndex+1,
				session.seed,
			)
			properties := newProperties()
			properties.setString("description", session.content.Paragraph())
			properties.setBool("qa_checkbox", containerIndex%2 == 0)
			byName[name] = discoveryDefinition{
				token: actor.Token,
				arguments: ToolArguments{
					WorkspaceReference: workspace.Slug,
					ProjectReference:   projectReference,
					Name:               name,
					Properties:         properties,
				},
			}
		}
		definitions[singular] = byName
	}
	return definitions
}

func seededIssueDefinitions(
	session *runSession,
	workspace WorkspaceIdentity,
	projectIndex int,
	projectReference string,
	projectRawID string,
	containers []soakNode,
) map[string]discoveryDefinition {
	definitions := make(map[string]discoveryDefinition, session.scale.IssuesPerProject)
	for issueIndex := range session.scale.IssuesPerProject {
		globalIndex := projectIndex*session.scale.IssuesPerProject + issueIndex
		actor := workspace.Actors[globalIndex%len(workspace.Actors)]
		name := session.content.IssueTitle(globalIndex)
		properties := issueProperties(
			session.content,
			actor,
			globalIndex,
			session.content.ReferenceTime(),
		)
		name = InjectEdgeCases(
			globalIndex,
			name,
			properties,
			session.content.ReferenceTime(),
		)
		parentReference := projectRawID
		if len(containers) > 0 && issueIndex%3 != 0 {
			parentReference = containers[issueIndex%len(containers)].RawID
			properties.setString("parent_id", parentReference)
		}
		definitions[name] = discoveryDefinition{
			token: actor.Token,
			arguments: ToolArguments{
				WorkspaceReference: workspace.Slug,
				ProjectReference:   projectReference,
				Name:               name,
				Properties:         properties,
			},
		}
		consumeSeededIssueDetails(session, globalIndex)
	}
	return definitions
}

func consumeSeededIssueDetails(session *runSession, issueIndex int) {
	childCount := session.scale.CommentsPerIssue + session.scale.ActivitiesPerIssue
	for range childCount {
		session.content.Name()
		session.content.Comment()
		session.content.Sentence()
	}
	if issueIndex%5 == 0 {
		session.content.Comment()
	}
}

func discoveryDefinitionFor(
	definitions map[string]discoveryDefinition,
	name string,
) *discoveryDefinition {
	definition, ok := definitions[name]
	if !ok {
		return nil
	}
	return &definition
}
