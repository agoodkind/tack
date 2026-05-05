package ops

import (
	"fmt"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
)

type repairCheck struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Details string `json:"details,omitempty"`
}

type repairValidateResult struct {
	Command     string         `json:"command"`
	NodeID      string         `json:"node_id"`
	RepairClass RepairClass    `json:"repair_class"`
	Status      string         `json:"status"`
	Summary     string         `json:"summary"`
	Checks      []repairCheck  `json:"checks"`
	Preview     *RepairPreview `json:"preview"`
}

type repairVerifyResult struct {
	Command     string                           `json:"command"`
	NodeID      uuid.UUID                        `json:"node_id"`
	RepairClass RepairClass                      `json:"repair_class,omitempty"`
	Status      string                           `json:"status"`
	Checks      []repairCheck                    `json:"checks"`
	Warnings    []string                         `json:"warnings,omitempty"`
	Records     *fdbadapter.NodeInspectionReport `json:"records,omitempty"`
}

type previewOutput struct {
	Command      string         `json:"command"`
	RepairClass  RepairClass    `json:"repair_class"`
	Status       string         `json:"status"`
	Summary      string         `json:"summary"`
	Preview      *RepairPreview `json:"preview"`
	ApplyCommand string         `json:"apply_command,omitempty"`
	SafeMode     string         `json:"safe_mode"`
}

func repairInspectionChecks(selectedNode *repairSelectedNode, report *fdbadapter.NodeInspectionReport) []repairCheck {
	checks := []repairCheck{
		{Name: "resolve_row_present", OK: report.Resolve != nil, Details: ""},
		{Name: "single_primary_row", OK: len(report.NodeInstances) == 1, Details: fmt.Sprintf("count=%d", len(report.NodeInstances))},
		{Name: "single_view_row", OK: len(report.NodeViews) == 1, Details: fmt.Sprintf("count=%d", len(report.NodeViews))},
		{Name: "selected_payload_available", OK: selectedNode.Node != nil, Details: ""},
	}
	if report.Resolve != nil && selectedNode.NodeView != nil {
		checks = append(checks,
			repairCheck{Name: "resolve_org_matches_view", OK: report.Resolve.OrgID == selectedNode.NodeView.OrgID, Details: ""},
			repairCheck{Name: "resolve_type_matches_view", OK: report.Resolve.NodeType == selectedNode.NodeView.NodeType, Details: ""},
		)
	}
	for _, relationship := range report.Relationships {
		checks = append(checks, repairCheck{
			Name:    "relationship_reverse_row_present",
			OK:      relationship.ReverseRowPresent,
			Details: relationship.RelationType + " -> " + relationship.TargetID.String(),
		})
	}
	for _, reverseRow := range report.RelationshipReverseRows {
		checks = append(checks, repairCheck{
			Name:    "relationship_forward_row_present",
			OK:      reverseRow.ForwardRowPresent,
			Details: reverseRow.RelationType + " <- " + reverseRow.SourceID.String(),
		})
	}
	return checks
}
