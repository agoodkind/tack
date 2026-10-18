package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/domain/node"
)

// verifyDatagenRepair requires each remaining repaired node to belong to
// the datagen organization, have its selected parent_id, and have exactly
// one child_of edge to that parent. It requires the legacy status issue to
// have the Done state and priority high and to omit status. It requires the
// smoke comment view to be absent and the export file of each delete entry
// to have mode 0600.
func verifyDatagenRepair(ctx context.Context, env *Env, fixture datagen.RepairFixture, manifest NodeIntegrityManifest) error {
	expected := map[uuid.UUID]uuid.UUID{
		fixture.MultipleEdges: fixture.EpicID, fixture.LegacyStatus: fixture.ProjectID, fixture.PrintedParent: fixture.EpicID,
		fixture.AbsentParent: fixture.ProjectID, fixture.Notice: fixture.NoticeTarget,
	}
	for nodeID, parentID := range expected {
		if err := requireDatagenRepairParent(ctx, env, fixture.OrgID, nodeID, parentID); err != nil {
			return err
		}
	}
	legacy, err := env.Stores.Views.Get(ctx, fixture.LegacyStatus)
	if err != nil || legacy == nil {
		return datagenRepairFailure(ctx, "read issue "+fixture.LegacyStatus.String(), errDatagenRepairMissing(err))
	}
	_, statusKept := legacy.Props["status"]
	if statusKept || datagenRepairProp(legacy.Props, "state_id") != fixture.DoneState.String() || datagenRepairProp(legacy.Props, "priority") != "high" {
		return datagenRepairFailure(ctx, "verify issue "+fixture.LegacyStatus.String(), fmt.Errorf("properties %v, want the Done state, priority high, and no status", legacy.Props))
	}
	smoke, err := env.Stores.Views.Get(ctx, fixture.SmokeComment)
	if err != nil {
		return datagenRepairFailure(ctx, "read the deleted comment "+fixture.SmokeComment.String(), err)
	}
	if smoke != nil {
		return datagenRepairFailure(ctx, "verify the deleted comment "+fixture.SmokeComment.String(), errors.New("the comment view still exists"))
	}
	for _, entry := range manifest.Entries {
		if entry.Delete == nil {
			continue
		}
		info, err := os.Stat(entry.Delete.ExportFile)
		if err != nil {
			return datagenRepairFailure(ctx, "read export "+entry.Delete.ExportFile, err)
		}
		if info.Mode().Perm() != repairExportMode {
			return datagenRepairFailure(ctx, "verify export "+entry.Delete.ExportFile, fmt.Errorf("file mode %v, want %v", info.Mode().Perm(), os.FileMode(repairExportMode)))
		}
	}
	return nil
}

// requireDatagenRepairParent reads the view and child_of edges of nodeID.
// It requires the view to belong to orgID and have parentID as parent_id,
// and requires exactly one child_of edge to parentID.
func requireDatagenRepairParent(ctx context.Context, env *Env, orgID, nodeID, parentID uuid.UUID) error {
	view, err := env.Stores.Views.Get(ctx, nodeID)
	if err != nil || view == nil {
		return datagenRepairFailure(ctx, "read node "+nodeID.String(), errDatagenRepairMissing(err))
	}
	edges, err := env.Stores.Relationships.ListBySource(ctx, orgID, nodeID, node.RelChildOf)
	if err != nil {
		return datagenRepairFailure(ctx, "list the child_of edges of node "+nodeID.String(), err)
	}
	if view.OrgID != orgID || datagenRepairProp(view.Props, repairParentProperty) != parentID.String() || len(edges) != 1 || edges[0].TargetID != parentID {
		return datagenRepairFailure(ctx, "verify node "+nodeID.String(),
			fmt.Errorf("organization %s, parent_id %s, and %d child_of edges, want organization %s and one parent %s", view.OrgID, view.Props[repairParentProperty], len(edges), orgID, parentID))
	}
	return nil
}

func datagenRepairProp(props map[string]json.RawMessage, name string) string {
	var text string
	if json.Unmarshal(props[name], &text) != nil {
		return ""
	}
	return text
}

func errDatagenRepairMissing(err error) error {
	if err != nil {
		return err
	}
	return errors.New("the record does not exist")
}
