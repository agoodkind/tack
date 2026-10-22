package datagen

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// unassignedText is the Assignees value tack_get_issue prints for an issue
// without an assigned_to relationship.
const unassignedText = "unassigned"

// errAssignmentStillRendered reports that tack_get_issue still showed an
// assignee after tack_remove_relationship removed the issue's assigned_to
// relationship.
var errAssignmentStillRendered = errors.New("tack_get_issue still lists an assignee after the assignment was removed")

// exerciseAssignmentRemoval removes the issue's assigned_to relationship
// to the actor, checks that tack_get_issue shows Assignees as "unassigned",
// then adds the relationship again to assign the issue to the actor.
// A dry run plans the calls and skips the check.
func (g *Generator) exerciseAssignmentRemoval(
	ctx context.Context,
	actor Actor,
	workspace WorkspaceIdentity,
	issueReference string,
) error {
	assignee := actor.UserID.String()
	if err := g.callRelationship(
		ctx, actor.Token, "tack_remove_relationship", issueReference, "assigned_to", assignee,
	); err != nil {
		return err
	}
	result, err := g.driver.Call(ctx, actor.Token, "tack_get_issue", ToolArguments{
		WorkspaceReference: workspace.Slug, ProjectReference: "", IssueReference: "", Name: "", Properties: nil,
		NodeID: issueReference, Query: "", NodeType: "", Direction: "", SourceID: "", RelationType: "", TargetID: "",
	})
	if err != nil {
		return loggedError(ctx, "qa datagen: tack_get_issue after assignment removal", err)
	}
	if !g.dryRun && result.field("Assignees") != unassignedText {
		message := fmt.Sprintf("qa datagen: assignment removal probe for issue %s", issueReference)
		return loggedError(ctx, message, errAssignmentStillRendered)
	}
	slog.InfoContext(ctx, "qa.datagen.assignment_removed",
		slog.String("issue", issueReference), slog.String("assignee", assignee))
	return g.callRelationship(
		ctx, actor.Token, "tack_add_relationship", issueReference, "assigned_to", assignee,
	)
}
