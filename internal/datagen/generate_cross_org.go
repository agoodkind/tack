package datagen

import (
	"context"
	"fmt"
	"log/slog"
)

// probeCrossOrgIsolation checks the organization membership gate with real
// requests. An actor from another organization attempts to read the first
// organization's node by UUID through generic tools. The gate must reject that
// access. The probe uses the first generated node regardless of its type.
//
// The helper accepts any tool error as a refusal. It does not verify that the
// error reports an authorization failure.
func (g *Generator) probeCrossOrgIsolation(ctx context.Context) error {
	foreign := g.foreignWorkspace()
	if foreign == nil {
		slog.InfoContext(
			ctx,
			"qa.datagen.cross_org_probe_skipped",
			slog.String("reason", "the scale generates a single org"),
		)
		return nil
	}
	if g.probeNodeID == "" {
		slog.InfoContext(
			ctx,
			"qa.datagen.cross_org_probe_skipped",
			slog.String("reason", "no generated node id is available"),
		)
		return nil
	}
	actor := foreign.Actors[0]
	probedTools := []string{"tack_get_properties", "tack_list_relationships"}
	for _, toolName := range probedTools {
		_, err := g.driver.Call(
			ctx,
			actor.Token,
			toolName,
			ToolArguments{
			NodeID: g.probeNodeID,
			},
		)
		if g.dryRun {
			continue
		}
		if err == nil {
			return fmt.Errorf(
				"qa datagen: cross-org probe: %s served node %s to an actor outside its org",
				toolName,
				g.probeNodeID,
			)
		}
	}
	slog.InfoContext(
		ctx,
		"qa.datagen.cross_org_probe_refused",
		slog.Int("tool_count", len(probedTools)),
		slog.String("node_id", g.probeNodeID),
		slog.Bool("dry_run", g.dryRun),
	)
	return nil
}

// foreignWorkspace returns the first workspace that has an actor and belongs to
// an organization other than the first workspace's organization. It returns nil
// when the workspace list is empty or a matching workspace does not exist.
func (g *Generator) foreignWorkspace() *WorkspaceIdentity {
	if len(g.identities.Workspaces) == 0 {
		return nil
	}
	firstOrg := g.identities.Workspaces[0].OrgID
	for index := range g.identities.Workspaces {
		workspace := &g.identities.Workspaces[index]
		if workspace.OrgID != firstOrg && len(workspace.Actors) > 0 {
			return workspace
		}
	}
	return nil
}
