package datagen

import (
	"context"
	"fmt"
	"time"
)

// soakOperationNames labels the latency of each operation kind, in mix
// order. The project read stays last.
var soakOperationNames = [...]string{
	"workflow", "issue_churn", "comment_create", "activity_create",
	"epic_churn", "cycle_churn", "module_churn", "label_churn",
	"state_churn", "project_churn", "relationship_churn", "issue_update",
	"cycle_update", soakKindSearch, "project_read",
}

const soakOperationKinds = len(soakOperationNames)

type soakNodeType string

const (
	soakNodeEpic   soakNodeType = "epic"
	soakNodeCycle  soakNodeType = "cycle"
	soakNodeModule soakNodeType = "module"
)

// executeOperation runs one operation and records its wall time under its
// kind when it completes.
func (s *Soak) executeOperation(ctx context.Context, operationIndex int) error {
	startedAt := s.clock.Now()
	kind, err := s.dispatchOperation(ctx, operationIndex)
	if err != nil {
		return err
	}
	s.latencies.add(kind, s.clock.Since(startedAt))
	return nil
}

func (s *Soak) dispatchOperation(ctx context.Context, operationIndex int) (string, error) {
	operationKind := operationIndex % soakOperationKinds
	operationCycle := operationIndex / soakOperationKinds
	project := s.projects[(operationCycle+operationKind)%len(s.projects)]
	actor := project.Workspace.Actors[operationIndex%len(project.Workspace.Actors)]
	create := operationCycle%2 == 0
	name := soakOperationNames[operationKind]
	switch operationKind {
	case 0:
		return name, s.advanceWorkflow(ctx, project, actor, operationIndex)
	case 1:
		return name, s.churnIssue(ctx, project, actor, operationIndex, create)
	case 2:
		return name, s.createIssueChildForProject(ctx, project, actor, "comment", operationIndex)
	case 3:
		return name, s.createIssueChildForProject(ctx, project, actor, "activity", operationIndex)
	case 4:
		return name, s.churnContainer(ctx, project, actor, soakNodeEpic, &project.Epics, operationIndex, create)
	case 5:
		return name, s.churnContainer(ctx, project, actor, soakNodeCycle, &project.Cycles, operationIndex, create)
	case 6:
		return name, s.churnContainer(ctx, project, actor, soakNodeModule, &project.Modules, operationIndex, create)
	case 7:
		return name, s.churnLabel(ctx, project, actor, operationIndex, create)
	case 8:
		return name, s.churnState(ctx, project, actor, operationIndex, create)
	case 9:
		return name, s.churnProject(ctx, project, actor, operationIndex, create)
	case 10:
		return name, s.churnRelationship(ctx, project, actor, operationIndex)
	case 11:
		return name, s.updateIssue(ctx, project, actor, operationIndex)
	case 12:
		return name, s.updateContainer(ctx, project, actor, soakNodeCycle, project.Cycles, operationIndex)
	case 13:
		return s.searchProject(ctx, project, actor, operationIndex)
	case 14:
		return name, s.readProject(ctx, project, actor, operationIndex)
	default:
		return "", fmt.Errorf("qa datagen soak: invalid operation index %d", operationIndex)
	}
}

func (s *Soak) updateIssue(
	ctx context.Context,
	project *soakProject,
	actor Actor,
	operationIndex int,
) error {
	if len(project.Issues) == 0 {
		return s.churnIssue(ctx, project, actor, operationIndex, true)
	}
	issue := project.Issues[operationIndex/soakOperationKinds%len(project.Issues)]
	properties := soakProperties(s.content, operationIndex)
	_, err := s.driver.Call(ctx, actor.Token, "tack_update_issue", ToolArguments{
		WorkspaceReference: project.Workspace.Slug,
		NodeID:             issue.RawID,
		Properties:         properties,
	})
	if err != nil {
		return loggedError(ctx, "qa datagen soak: update issue "+issue.RawID, err)
	}
	s.summary.Updated++
	return nil
}

func (s *Soak) updateContainer(
	ctx context.Context,
	project *soakProject,
	actor Actor,
	nodeType soakNodeType,
	nodes []soakNode,
	operationIndex int,
) error {
	if len(nodes) == 0 {
		return s.churnContainer(
			ctx, project, actor, nodeType, containerNodes(project, nodeType),
			operationIndex, true,
		)
	}
	target := nodes[operationIndex/soakOperationKinds%len(nodes)]
	properties := soakProperties(s.content, operationIndex)
	if nodeType == soakNodeCycle {
		simulated := s.content.ReferenceTime().Add(
			time.Duration(operationIndex) * 24 * time.Hour,
		)
		properties.setString("start_date", simulated.Format(time.RFC3339))
		properties.setString(
			"due_date",
			simulated.Add(14*24*time.Hour).Format(time.RFC3339),
		)
	}
	_, err := s.driver.Call(
		ctx,
		actor.Token,
		"tack_update_"+string(nodeType),
		ToolArguments{
			WorkspaceReference: project.Workspace.Slug,
			NodeID:             target.RawID,
			Properties:         properties,
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

func soakProperties(content *Content, operationIndex int) NodeProperties {
	properties := newProperties()
	properties.setString("description", content.Paragraph())
	properties.setString("qa_text", content.Comment())
	properties.setInt("qa_number", operationIndex)
	properties.setBool("qa_checkbox", operationIndex%2 == 0)
	properties.setString(
		"qa_select",
		[]string{"planned", "active", "verified"}[operationIndex%3],
	)
	return properties
}

func containerNodes(project *soakProject, nodeType soakNodeType) *[]soakNode {
	switch nodeType {
	case soakNodeEpic:
		return &project.Epics
	case soakNodeCycle:
		return &project.Cycles
	default:
		return &project.Modules
	}
}
