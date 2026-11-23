package datagen

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
)

// cascadeCountText is the field of the tack_delete_project output that
// reports how many nodes the delete removed.
const cascadeCountText = "Deleted nodes: "

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
		slog.InfoContext(
			ctx,
			"qa.datagen.cascade_probe_skipped",
			slog.String("reason", "the scale generates no actor"),
		)
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
	project, err := g.driver.Call(
		ctx,
		token,
		"tack_create_project",
		ToolArguments{
			WorkspaceReference: workspace.Slug,
			Name:               name,
			Properties:         properties,
		},
	)
	if err != nil {
		return loggedError(ctx, "qa datagen: create cascade probe project", err)
	}
	issue, err := g.driver.Call(
		ctx,
		token,
		"tack_create_issue",
		ToolArguments{
			WorkspaceReference: workspace.Slug,
			ProjectReference:   identifier,
			Name:               "QA cascade delete issue",
		},
	)
	if err != nil {
		return loggedError(ctx, "qa datagen: create cascade probe issue", err)
	}
	comment, err := g.driver.Call(
		ctx,
		token,
		"tack_create_comment",
		ToolArguments{
			WorkspaceReference: workspace.Slug,
			ProjectReference:   identifier,
			IssueReference:     issue.RawID(),
			Name:               "QA cascade delete comment",
		},
	)
	if err != nil {
		return loggedError(ctx, "qa datagen: create cascade probe comment", err)
	}
	moved, err := g.probeCycleMove(ctx, token, workspace.Slug, identifier)
	if err != nil {
		return err
	}
	deleted, err := g.driver.Call(
		ctx,
		token,
		"tack_delete_project",
		ToolArguments{
			WorkspaceReference: workspace.Slug,
			NodeID:             project.RawID(),
		},
	)
	if err != nil {
		return loggedError(ctx, "qa datagen: delete cascade probe project "+identifier, err)
	}
	if g.dryRun {
		return nil
	}
	if !strings.Contains(deleted.Text(), cascadeCountText) {
		return loggedError(
			ctx,
			"qa datagen: delete output of project "+identifier,
			fmt.Errorf("output has no %q field:\n%s", cascadeCountText, deleted.Text()),
		)
	}
	if err := recordCascadeDelete(ctx, proof, deleted, project.RawID()); err != nil {
		return err
	}
	return g.requireCascadeGone(
		ctx,
		token,
		workspace.Slug,
		identifier,
		deleted,
		project.RawID(),
		[]string{issue.RawID(), moved},
		comment.RawID(),
	)
}
