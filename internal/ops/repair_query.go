package ops

import (
	"context"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/domain/node"
)

func init() {
	Register(Operation{
		Name:        "repair.query",
		Description: "Query all core record families for TACK_REPAIR_NODE_ID and print a deterministic raw inspection report.",
		Run:         runRepairQuery,
	})
}

func runRepairQuery(ctx context.Context, env *Env) error {
	nodeID, err := readRequiredUUIDEnv(repairNodeIDEnv)
	if err != nil {
		return err
	}
	report, err := env.Stores.Inspect.QueryNodeRecords(ctx, nodeID)
	if err != nil {
		return err
	}
	result := &repairQueryResult{
		NodeID:                nodeID,
		Selection:             selectRepairNode(report),
		Raw:                   report,
		IndexedPropertyChecks: nil,
		Warnings:              make([]string, 0),
	}
	selectedNode := result.Selection.Node
	if selectedNode == nil {
		result.Warnings = append(result.Warnings, "indexed property checks skipped because no canonical node payload was selected")
		return writeRepairOutput(result)
	}
	propertyDefs, err := env.Stores.PropertyDefs.List(ctx, selectedNode.OrgID)
	if err != nil {
		return err
	}
	result.IndexedPropertyChecks = buildIndexedPropertyChecks(selectedNode, propertyDefs, report)
	return writeRepairOutput(result)
}

func buildIndexedPropertyChecks(currentNode *node.Node, defs []*node.PropertyDef, report *fdbadapter.NodeInspectionReport) []repairIndexedPropertyCheck {
	applicable := make([]repairIndexedPropertyCheck, 0)
	for _, def := range defs {
		if !def.Indexed {
			continue
		}
		raw, ok := currentNode.Props[def.Name]
		if !ok || len(raw) == 0 {
			continue
		}
		matching := 0
		for _, row := range report.PropertyIndexRows {
			if row.OrgID != currentNode.OrgID {
				continue
			}
			if row.NodeType != currentNode.NodeType {
				continue
			}
			if row.PropertyName != def.Name {
				continue
			}
			if string(row.EncodedValue) == string(raw) {
				matching++
			}
		}
		applicable = append(applicable, repairIndexedPropertyCheck{
			PropertyName:     def.Name,
			ExpectedFromNode: true,
			MatchingRowCount: matching,
		})
	}
	return applicable
}
