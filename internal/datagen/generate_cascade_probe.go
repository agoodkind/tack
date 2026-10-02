package datagen

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
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

const (
	cascadeCompletionTimeout = 2 * time.Minute
	cascadePollInterval      = 250 * time.Millisecond
)

// errCascadeChildKept reports that a child of a deleted project still exists.
var errCascadeChildKept = errors.New("a child of the deleted project still exists")

// CascadeDeleteEvidence reports the initial response of one probe deletion.
type CascadeDeleteEvidence struct {
	RootReference string
	JobID         string
	InitialState  string
}

// CascadeProbeResult includes the probe and any interrupted-run cleanup deletion.
type CascadeProbeResult struct {
	Deletes []CascadeDeleteEvidence
}

// VerifyCascadeDelete creates and deletes the QA probe through authenticated MCP.
// Public reads must confirm the root and probe descendants are absent.
func (g *Generator) VerifyCascadeDelete(ctx context.Context) (CascadeProbeResult, error) {
	probe := *g
	probe.driver = NewDriver(g.driver.graph, g.dryRun, g.seed)
	if !g.dryRun {
		// Deleted probe nodes retain their idempotency records for 24 hours.
		probe.driver.sessionID += ":cascade:" + uuid.NewString()
	}
	registerIdentityTokens(probe.driver, g.identities)
	defer func() { g.driver.callCount.Add(probe.driver.CallCount()) }()
	result := CascadeProbeResult{Deletes: nil}
	err := probe.probeCascadeDelete(ctx, &result)
	return result, err
}

// probeCascadeDelete creates a project with an issue and a comment under the
// issue in the first workspace, runs probeCycleMove in the project, then
// deletes the project through tack_delete_project. The delete output must
// report the deleted node count, and tack_get_issue and tack_get_comment must
// report both issues and the comment missing.
// A project that an interrupted earlier run left behind is deleted first. A
// dry run plans the calls and skips the checks.
func (g *Generator) probeCascadeDelete(ctx context.Context, proof *CascadeProbeResult) error {
	if len(g.identities.Workspaces) == 0 || len(g.identities.Workspaces[0].Actors) == 0 {
		slog.InfoContext(ctx, "qa.datagen.cascade_probe_skipped", slog.String("reason", "the scale generates no actor"))
		return nil
	}
	workspace := g.identities.Workspaces[0]
	token := workspace.Actors[0].Token
	identifier := fmt.Sprintf("DEL%06X", uint64(g.seed)&0xFFFFFF)
	name := "QA cascade delete " + identifier
	if err := g.deleteResidualCascadeProject(ctx, token, workspace.Slug, name, proof); err != nil {
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
	if err := recordCascadeDelete(ctx, proof, deleted, project.RawID()); err != nil {
		return err
	}
	return g.requireCascadeGone(ctx, token, workspace.Slug, identifier, deleted, project.RawID(), []string{issue.RawID(), moved}, comment.RawID())
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
func (g *Generator) deleteResidualCascadeProject(ctx context.Context, token, workspaceReference, name string, proof *CascadeProbeResult) error {
	existing, err := g.driver.Call(ctx, token, "tack_list_projects", scopeArgs(workspaceReference, ""))
	if err != nil {
		return loggedError(ctx, "qa datagen: list projects for the cascade probe", err)
	}
	residual := existing.ReferenceForName(name)
	if residual == "" {
		return nil
	}
	deleted, err := g.driver.Call(ctx, token, "tack_delete_project", ToolArguments{
		WorkspaceReference: workspaceReference, NodeID: residual,
	})
	if err != nil {
		return loggedError(ctx, "qa datagen: delete residual cascade probe project "+residual, err)
	}
	if g.dryRun {
		return nil
	}
	if err := recordCascadeDelete(ctx, proof, deleted, residual); err != nil {
		return err
	}
	return g.requireCascadeGone(ctx, token, workspaceReference, residual, deleted, residual, nil, "")
}

func recordCascadeDelete(ctx context.Context, proof *CascadeProbeResult, deleted Result, root string) error {
	state := "finished"
	if strings.Contains(deleted.Text(), "- Delete state: running") {
		state = "running"
	} else if !strings.Contains(deleted.Text(), "- Delete state: finished") {
		return loggedError(ctx, "qa datagen: read cascade deletion state", errors.New("delete output has no recognized state"))
	}
	_, suffix, found := strings.Cut(deleted.Text(), "Delete job: `")
	jobID, _, terminated := strings.Cut(suffix, "`")
	if !found || !terminated || jobID == "" {
		return loggedError(ctx, "qa datagen: read cascade deletion job", errors.New("delete output has no job ID"))
	}
	proof.Deletes = append(proof.Deletes, CascadeDeleteEvidence{RootReference: root, JobID: jobID, InitialState: state})
	return nil
}

// requireCascadeGone waits for public reads after a running deletion. A
// finished deletion must already exclude the root and every probe child.
func (g *Generator) requireCascadeGone(ctx context.Context, token, workspaceReference, identifier string, deleted Result, projectID string, issueIDs []string, commentID string) error {
	background := strings.Contains(deleted.Text(), "- Delete state: running")
	if !background && !strings.Contains(deleted.Text(), "- Delete state: finished") {
		return loggedError(ctx, "qa datagen: read cascade deletion state", errors.New("delete output has no recognized state"))
	}
	ctx, cancel := context.WithTimeout(ctx, cascadeCompletionTimeout)
	defer cancel()
	children := []struct {
		tool  string
		rawID string
	}{
		{tool: "tack_get_project", rawID: projectID},
	}
	for _, issueID := range issueIDs {
		children = append(children, struct{ tool, rawID string }{tool: "tack_get_issue", rawID: issueID})
	}
	if commentID != "" {
		children = append(children, struct{ tool, rawID string }{tool: "tack_get_comment", rawID: commentID})
	}
	ticker := time.NewTicker(cascadePollInterval)
	defer ticker.Stop()
	for {
		remaining := ""
		for _, child := range children {
			_, err := g.driver.Call(ctx, token, child.tool, ToolArguments{WorkspaceReference: workspaceReference, NodeID: child.rawID})
			message := fmt.Sprintf("qa datagen: %s %s after the project delete", child.tool, child.rawID)
			if err == nil {
				remaining = message
				continue
			}
			if !strings.Contains(err.Error(), notFoundText) {
				return loggedError(ctx, message, err)
			}
		}
		if remaining == "" {
			break
		}
		if !background {
			return loggedError(ctx, remaining, errCascadeChildKept)
		}
		select {
		case <-ctx.Done():
			return loggedError(ctx, remaining, errors.Join(errCascadeChildKept, ctx.Err()))
		case <-ticker.C:
		}
	}
	slog.InfoContext(ctx, "qa.datagen.cascade_delete_verified", slog.String("project", identifier))
	return nil
}
