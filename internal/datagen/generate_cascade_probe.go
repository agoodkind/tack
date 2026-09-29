package datagen

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// cascadeCountText is the field of the tack_delete_project output that
// reports how many nodes the delete removed.
const cascadeCountText = "Deleted nodes: "

// movedCountText is the field of the tack_delete_cycle output that reports
// one issue moved to the project.
const movedCountText = "Moved nodes: 1"

// notFoundText is part of the error that a get tool returns for a missing
// node.
const notFoundText = "not found"

// errCascadeChildKept reports that a child of a deleted project still exists.
var errCascadeChildKept = errors.New("a child of the deleted project still exists")

// probeCascadeDelete creates a project with an issue and a comment under the
// issue in the first workspace, runs probeCycleMove in the project, then
// deletes the project through tack_delete_project. The delete output must
// report the deleted node count, and tack_get_issue and tack_get_comment must
// report both issues and the comment missing.
// A project that an interrupted earlier run left behind is deleted first. A
// dry run plans the calls and skips the checks.
func (g *Generator) probeCascadeDelete(ctx context.Context) error {
	if len(g.identities.Workspaces) == 0 || len(g.identities.Workspaces[0].Actors) == 0 {
		slog.InfoContext(ctx, "qa.datagen.cascade_probe_skipped", slog.String("reason", "the scale generates no actor"))
		return nil
	}
	workspace := g.identities.Workspaces[0]
	token := workspace.Actors[0].Token
	identifier := fmt.Sprintf("DEL%06X", uint64(g.seed)&0xFFFFFF)
	name := "QA cascade delete " + identifier
	if err := g.deleteResidualCascadeProject(ctx, token, workspace.Slug, name); err != nil {
		return err
	}
	properties := newProperties()
	properties.setString("identifier", identifier)
	properties.setString("slug", slugify(identifier))
	project, err := g.driver.Call(ctx, token, "tack_create_project", ToolArguments{
		WorkspaceReference: workspace.Slug, Name: name, Properties: properties,
	})
	if err != nil {
		return loggedError(ctx, "qa datagen: create cascade probe project", err)
	}
	issue, err := g.driver.Call(ctx, token, "tack_create_issue", ToolArguments{
		WorkspaceReference: workspace.Slug, ProjectReference: identifier, Name: "QA cascade delete issue",
	})
	if err != nil {
		return loggedError(ctx, "qa datagen: create cascade probe issue", err)
	}
	comment, err := g.driver.Call(ctx, token, "tack_create_comment", ToolArguments{
		WorkspaceReference: workspace.Slug, ProjectReference: identifier, IssueReference: issue.RawID(),
		Name: "QA cascade delete comment",
	})
	if err != nil {
		return loggedError(ctx, "qa datagen: create cascade probe comment", err)
	}
	moved, err := g.probeCycleMove(ctx, token, workspace.Slug, identifier)
	if err != nil {
		return err
	}
	deleted, err := g.driver.Call(ctx, token, "tack_delete_project", ToolArguments{
		WorkspaceReference: workspace.Slug, NodeID: project.RawID(),
	})
	if err != nil {
		return loggedError(ctx, "qa datagen: delete cascade probe project "+identifier, err)
	}
	if g.dryRun {
		return nil
	}
	if !strings.Contains(deleted.Text(), cascadeCountText) {
		return loggedError(ctx, "qa datagen: delete output of project "+identifier, fmt.Errorf("output has no %q field:\n%s", cascadeCountText, deleted.Text()))
	}
	if err := g.requireCascadeChildrenGone(ctx, token, workspace.Slug, identifier, issue.RawID(), comment.RawID()); err != nil {
		return err
	}
	return g.requireCascadeChildrenGone(ctx, token, workspace.Slug, identifier, moved, comment.RawID())
}

// probeCycleMove creates a cycle in the probe project and an issue with the
// cycle as its parent, then deletes the cycle through tack_delete_cycle. The
// delete must move the issue to the project: the output must report one
// moved node, and tack_get_issue must still return the issue. It returns
// the issue's raw id. A dry run plans the calls and skips the checks.
func (g *Generator) probeCycleMove(ctx context.Context, token, workspaceReference, identifier string) (string, error) {
	cycle, err := g.driver.Call(ctx, token, "tack_create_cycle", ToolArguments{
		WorkspaceReference: workspaceReference, ProjectReference: identifier, Name: "QA cascade delete cycle",
	})
	if err != nil {
		return "", loggedError(ctx, "qa datagen: create cascade probe cycle", err)
	}
	properties := newProperties()
	properties.setString("parent_id", cycle.RawID())
	issue, err := g.driver.Call(ctx, token, "tack_create_issue", ToolArguments{
		WorkspaceReference: workspaceReference, ProjectReference: identifier, Name: "QA cascade delete moved issue",
		Properties: properties,
	})
	if err != nil {
		return "", loggedError(ctx, "qa datagen: create cascade probe issue in the cycle", err)
	}
	deleted, err := g.driver.Call(ctx, token, "tack_delete_cycle", ToolArguments{
		WorkspaceReference: workspaceReference, NodeID: cycle.RawID(),
	})
	if err != nil {
		return "", loggedError(ctx, "qa datagen: delete cascade probe cycle", err)
	}
	if g.dryRun {
		return issue.RawID(), nil
	}
	if !strings.Contains(deleted.Text(), movedCountText) {
		return "", loggedError(ctx, "qa datagen: delete output of cascade probe cycle", fmt.Errorf("output has no %q field:\n%s", movedCountText, deleted.Text()))
	}
	if _, err := g.driver.Call(ctx, token, "tack_get_issue", ToolArguments{WorkspaceReference: workspaceReference, NodeID: issue.RawID()}); err != nil {
		return "", loggedError(ctx, "qa datagen: get the issue the cycle delete moved", err)
	}
	return issue.RawID(), nil
}

// deleteResidualCascadeProject deletes the probe project named name when an
// interrupted earlier run left it in the workspace.
func (g *Generator) deleteResidualCascadeProject(ctx context.Context, token, workspaceReference, name string) error {
	existing, err := g.driver.Call(ctx, token, "tack_list_projects", scopeArgs(workspaceReference, ""))
	if err != nil {
		return loggedError(ctx, "qa datagen: list projects for the cascade probe", err)
	}
	residual := existing.ReferenceForName(name)
	if residual == "" {
		return nil
	}
	if _, err := g.driver.Call(ctx, token, "tack_delete_project", ToolArguments{
		WorkspaceReference: workspaceReference, NodeID: residual,
	}); err != nil {
		return loggedError(ctx, "qa datagen: delete residual cascade probe project "+residual, err)
	}
	return nil
}

// requireCascadeChildrenGone requires tack_get_issue and tack_get_comment to
// report the probe's issue and comment missing.
func (g *Generator) requireCascadeChildrenGone(ctx context.Context, token, workspaceReference, identifier, issueID, commentID string) error {
	children := []struct {
		tool  string
		rawID string
	}{
		{tool: "tack_get_issue", rawID: issueID},
		{tool: "tack_get_comment", rawID: commentID},
	}
	for _, child := range children {
		_, err := g.driver.Call(ctx, token, child.tool, ToolArguments{WorkspaceReference: workspaceReference, NodeID: child.rawID})
		message := fmt.Sprintf("qa datagen: %s %s after the project delete", child.tool, child.rawID)
		if err == nil {
			return loggedError(ctx, message, errCascadeChildKept)
		}
		if !strings.Contains(err.Error(), notFoundText) {
			return loggedError(ctx, message, err)
		}
	}
	slog.InfoContext(ctx, "qa.datagen.cascade_delete_verified", slog.String("project", identifier))
	return nil
}
