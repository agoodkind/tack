package repair

import (
	"encoding/json"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/domain/node"
)

type selectedNode struct {
	Node               *node.Node        `json:"node,omitempty"`
	NodeID             string            `json:"node_id"`
	NodeInstanceCount  int               `json:"node_instance_count"`
	NodeView           *node.NodeView    `json:"node_view,omitempty"`
	NodeViewCount      int               `json:"node_view_count"`
	OutgoingRelCount   int               `json:"outgoing_relationship_count"`
	PropertyIndexCount int               `json:"property_index_count"`
	Resolve            *node.NodeResolve `json:"resolve,omitempty"`
	SlugRowCount       int               `json:"slug_row_count"`
	IncomingRelCount   int               `json:"incoming_relationship_count"`
	Warnings           []string          `json:"warnings,omitempty"`
}

func selectNode(report *fdbadapter.NodeInspectionReport) *selectedNode {
	selected := &selectedNode{
		NodeID:             report.NodeID.String(),
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
		Props:     cloneRepairProps(view.Props),
		CreatedBy: view.CreatedBy,
		UpdatedBy: view.UpdatedBy,
		CreatedAt: view.CreatedAt,
		UpdatedAt: view.UpdatedAt,
	}
}

func cloneRepairProps(input map[string]json.RawMessage) map[string]json.RawMessage {
	if input == nil {
		return nil
	}
	cloned := make(map[string]json.RawMessage, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}
