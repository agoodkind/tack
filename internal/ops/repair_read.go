package ops

import (
	"context"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/domain/node"
)

func init() {
	Register(Operation{
		Name:        "repair.read",
		Description: "Read one node by UUID from TACK_REPAIR_NODE_ID and print a deterministic inspection summary.",
		Run:         runRepairRead,
	})
}

func runRepairRead(ctx context.Context, env *Env) error {
	nodeID, err := readRequiredUUIDEnv(repairNodeIDEnv)
	if err != nil {
		return err
	}
	report, err := env.Stores.Inspect.QueryNodeRecords(ctx, nodeID)
	if err != nil {
		return err
	}
	return writeRepairOutput(selectRepairNode(report))
}

func selectRepairNode(report *fdbadapter.NodeInspectionReport) *repairSelectedNode {
	selected := &repairSelectedNode{
		NodeID:             report.NodeID,
		Resolve:            report.Resolve,
		NodeInstanceCount:  len(report.NodeInstances),
		NodeViewCount:      len(report.NodeViews),
		PropertyIndexCount: len(report.PropertyIndexRows),
		SlugRowCount:       len(report.SlugRows),
		OutgoingRelCount:   len(report.Relationships),
		IncomingRelCount:   len(report.RelationshipReverseRows),
		Warnings:           make([]string, 0),
	}
	if report.Resolve == nil {
		selected.Warnings = append(selected.Warnings, "node_resolve row missing")
	}
	if len(report.NodeInstances) == 0 {
		selected.Warnings = append(selected.Warnings, "node_instance row missing")
		selected.Node = pickNodeFromView(report)
	} else {
		if len(report.NodeInstances) > 1 {
			selected.Warnings = append(selected.Warnings, "multiple node_instance rows found for node UUID")
		}
		selected.Node = report.NodeInstances[0].Value
	}
	if len(report.NodeViews) == 0 {
		selected.Warnings = append(selected.Warnings, "node_view row missing")
	} else {
		if len(report.NodeViews) > 1 {
			selected.Warnings = append(selected.Warnings, "multiple node_view rows found for node UUID")
		}
		selected.NodeView = report.NodeViews[0].Value
	}
	if selected.Node == nil {
		selected.Warnings = append(selected.Warnings, "selected node payload unavailable")
	}
	return selected
}

func pickNodeFromView(report *fdbadapter.NodeInspectionReport) *node.Node {
	if len(report.NodeViews) == 0 || report.NodeViews[0].Value == nil {
		return nil
	}
	view := report.NodeViews[0].Value
	return &node.Node{
		ID:        view.ID,
		OrgID:     view.OrgID,
		NodeType:  view.NodeType,
		Name:      view.Name,
		Props:     cloneProps(view.Props),
		CreatedBy: view.CreatedBy,
		UpdatedBy: view.UpdatedBy,
		CreatedAt: view.CreatedAt,
		UpdatedAt: view.UpdatedAt,
	}
}
