package ops

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/domain/node"
)

func init() {
	Register(Operation{
		Name:        "repair.find",
		Description: "Find nodes by address or indexed property from TACK_REPAIR_FIND_* env vars and print deterministic inspection summaries.",
		Run:         runRepairFind,
	})
}

func runRepairFind(ctx context.Context, env *Env) error {
	log := env.Log
	mode, err := readRequiredStringEnv(repairFindModeEnv)
	if err != nil {
		return err
	}
	mode = strings.TrimSpace(mode)
	result := &repairFindResult{
		Mode:         mode,
		Input:        map[string]string{},
		Matches:      make([]repairSelectedNode, 0),
		Inspections:  nil,
		RemovalGates: nil,
		Warnings:     nil,
	}
	var nodeIDs []uuid.UUID
	if mode == "address" {
		nodeType, err := readRequiredStringEnv(repairFindNodeTypeEnv)
		if err != nil {
			return err
		}
		address, err := readRepairAddressEnv()
		if err != nil {
			return err
		}
		result.Input[repairFindNodeTypeEnv] = nodeType
		result.Input[repairFindAddressEnv] = address
		nodeID, err := env.Stores.Nodes.GetAddress(ctx, nodeType, node.AddressKindPrimary, address)
		if err != nil {
			log.ErrorContext(ctx, "repair.find.address",
				slog.String("node_type", nodeType),
				slog.String("address", address),
				slog.String("err", err.Error()),
			)
			return fmt.Errorf("get address %q for node type %q: %w", address, nodeType, err)
		}
		if nodeID != uuid.Nil {
			nodeIDs = append(nodeIDs, nodeID)
		}
	}
	if mode == "property" {
		orgID, err := readRequiredUUIDEnv(repairFindOrgIDEnv)
		if err != nil {
			return err
		}
		nodeType, err := readRequiredStringEnv(repairFindNodeTypeEnv)
		if err != nil {
			return err
		}
		propertyName, err := readRequiredStringEnv(repairFindPropEnv)
		if err != nil {
			return err
		}
		value, err := readRequiredJSONEnv(repairFindValueEnv)
		if err != nil {
			return err
		}
		result.Input[repairFindOrgIDEnv] = orgID.String()
		result.Input[repairFindNodeTypeEnv] = nodeType
		result.Input[repairFindPropEnv] = propertyName
		result.Input[repairFindValueEnv] = string(value)
		nodes, err := env.Stores.Nodes.ListByProperty(ctx, orgID, nodeType, propertyName, value)
		if err != nil {
			return err
		}
		nodeIDs = make([]uuid.UUID, 0, len(nodes))
		for _, currentNode := range nodes {
			nodeIDs = append(nodeIDs, currentNode.ID)
		}
	}
	if mode != "address" && mode != "property" {
		return fmt.Errorf("unsupported %s %q", repairFindModeEnv, mode)
	}
	sort.Slice(nodeIDs, func(i int, j int) bool {
		return nodeIDs[i].String() < nodeIDs[j].String()
	})
	result.Inspections = make([]fdbadapter.NodeInspectionReport, 0, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		report, err := env.Stores.Inspect.QueryNodeRecords(ctx, nodeID)
		if err != nil {
			return err
		}
		result.Inspections = append(result.Inspections, *report)
		result.Matches = append(result.Matches, *selectRepairNode(report))
	}
	if len(nodeIDs) == 0 {
		result.Warnings = append(result.Warnings, "no matching node UUIDs found")
	}
	return writeRepairOutput(result)
}
