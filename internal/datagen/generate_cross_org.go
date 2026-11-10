package datagen

import (
	"context"
	"fmt"
	"log/slog"
)

// probeCrossOrgIsolation verifies the org membership gate with real traffic:
// an actor from a different org reads a node of the first org by UUID through
// the generic UUID-path tools and must be refused. The probe is generic over
// node types; it uses whatever node the corpus created first.
func (g *Generator) probeCrossOrgIsolation(ctx context.Context) error {
	foreign := g.foreignWorkspace()
	if foreign == nil {
		slog.InfoContext(ctx, "qa.datagen.cross_org_probe_skipped",
			slog.String("reason", "the scale generates a single org"))
		return nil
	}
	if g.probeNodeID == "" {
		slog.InfoContext(ctx, "qa.datagen.cross_org_probe_skipped",
			slog.String("reason", "no generated node id is available"))
		return nil
	}
	actor := foreign.Actors[0]
	probedTools := []string{"tack_get_properties", "tack_list_relationships"}
	for _, toolName := range probedTools {
		_, err := g.driver.Call(ctx, actor.Token, toolName, ToolArguments{
			NodeID: g.probeNodeID,
		})
		if g.dryRun {
			continue
		}
		if err == nil {
			return fmt.Errorf(
				"qa datagen: cross-org probe: %s served node %s to an actor outside its org",
				toolName, g.probeNodeID,
			)
		}
	}
	slog.InfoContext(ctx, "qa.datagen.cross_org_probe_refused",
		slog.Int("tool_count", len(probedTools)), slog.String("node_id", g.probeNodeID),
		slog.Bool("dry_run", g.dryRun))
	return nil
}

// foreignWorkspace returns the first workspace whose org differs from the
// first workspace's org, or nil when every workspace shares one org.
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
