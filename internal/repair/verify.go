package repair

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/config"
)

type repairVerifyResult struct {
	Command     string                           `json:"command"`
	NodeID      uuid.UUID                        `json:"node_id"`
	RepairClass RepairClass                      `json:"repair_class,omitempty"`
	Status      string                           `json:"status"`
	Checks      []repairCheck                    `json:"checks"`
	Warnings    []string                         `json:"warnings,omitempty"`
	Records     *fdbadapter.NodeInspectionReport `json:"records,omitempty"`
}

func RunVerify(cfg *config.Config, argv []string) {
	runVerify(cfg, argv)
}

func runVerify(cfg *config.Config, argv []string) {
	flagSet := newFlagSet("verify")
	nodeIDRaw := flagSet.String("node", "", "node UUID")
	classRaw := flagSet.String("class", "", "optional repair class")
	includeRecords := flagSet.Bool("records", false, "include raw inspection rows")
	if err := flagSet.Parse(argv); err != nil {
		os.Exit(2)
	}
	if flagSet.NArg() != 0 {
		failUsage("verify", "verify: unexpected positional arguments")
	}
	if strings.TrimSpace(*nodeIDRaw) == "" {
		failUsage("verify", "verify: --node is required")
	}
	nodeID := mustParseUUID("verify", "--node", *nodeIDRaw)
	env := mustOpenEnv(cfg)
	defer env.Close()
	report, err := env.Stores.Inspect.QueryNodeRecords(context.Background(), nodeID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify: inspect node %s: %v\n", nodeID, err)
		os.Exit(1)
	}
	selectedNode := selectNode(report)
	checks := repairInspectionChecks(selectedNode, report)
	status := "ok"
	for _, check := range checks {
		if !check.OK {
			status = "failed"
			break
		}
	}
	result := repairVerifyResult{
		Command:  "verify",
		NodeID:   nodeID,
		Status:   status,
		Checks:   checks,
		Warnings: selectedNode.Warnings,
	}
	if strings.TrimSpace(*classRaw) != "" {
		repairClass := mustParseRepairClass("verify", *classRaw)
		result.RepairClass = repairClass
		console := newRepairConsoleFromEnv(env)
		preview, previewErr := console.Preview(context.Background(), RepairPreviewInput{Class: repairClass, NodeID: nodeID})
		if previewErr != nil {
			result.Status = "failed"
			result.Checks = append(result.Checks, repairCheck{Name: "repair_preview", OK: false, Details: previewErr.Error()})
		} else {
			result.Checks = append(result.Checks, repairCheck{Name: "repair_preview", OK: true, Details: repairPreviewStatus(preview) + ": " + preview.Summary})
		}
	}
	if *includeRecords {
		result.Records = report
	}
	writeJSON(result)
}

func repairInspectionChecks(selectedNode *selectedNode, report *fdbadapter.NodeInspectionReport) []repairCheck {
	checks := []repairCheck{
		{Name: "resolve_row_present", OK: report.Resolve != nil},
		{Name: "single_primary_row", OK: len(report.NodeInstances) == 1, Details: fmt.Sprintf("count=%d", len(report.NodeInstances))},
		{Name: "single_view_row", OK: len(report.NodeViews) == 1, Details: fmt.Sprintf("count=%d", len(report.NodeViews))},
		{Name: "selected_payload_available", OK: selectedNode.Node != nil},
	}
	if report.Resolve != nil && selectedNode.NodeView != nil {
		checks = append(checks,
			repairCheck{Name: "resolve_org_matches_view", OK: report.Resolve.OrgID == selectedNode.NodeView.OrgID},
			repairCheck{Name: "resolve_type_matches_view", OK: report.Resolve.NodeType == selectedNode.NodeView.NodeType},
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
