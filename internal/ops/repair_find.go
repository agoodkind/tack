package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
)

// repairFindMode identifies the node-lookup strategy for repair.find.
type repairFindMode string

const (
	repairFindModeAddress  repairFindMode = "address"
	repairFindModeProperty repairFindMode = "property"
)

func init() {
	Register(Operation{
		Name:        "repair.find",
		Audit:       audit.Spec{Verb: string(audit.VerbOpsInspectFind), Reads: true},
		Description: "Find nodes by address or indexed property from TACK_REPAIR_FIND_* env vars and print deterministic inspection summaries.",
		Run:         runRepairFind,
	})
}

func runRepairFind(ctx context.Context, env *Env) error {
	modeStr, err := readRequiredStringEnv(repairFindModeEnv)
	if err != nil {
		return err
	}
	mode := repairFindMode(strings.TrimSpace(modeStr))
	result := &repairFindResult{
		Mode:         string(mode),
		Input:        map[string]string{},
		Matches:      make([]repairSelectedNode, 0),
		Inspections:  nil,
		RemovalGates: nil,
		Warnings:     nil,
	}
	nodeIDs, err := findNodeIDsByMode(ctx, env, mode, result)
	if err != nil {
		return err
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

// findNodeIDsByMode dispatches to the mode-specific lookup and populates
// result.Input with the parameters that were read from the environment.
func findNodeIDsByMode(ctx context.Context, env *Env, mode repairFindMode, result *repairFindResult) ([]uuid.UUID, error) {
	switch mode {
	case repairFindModeAddress:
		return findNodeIDsByAddress(ctx, env, result)
	case repairFindModeProperty:
		return findNodeIDsByProperty(ctx, env, result)
	default:
		return nil, fmt.Errorf("unsupported %s %q", repairFindModeEnv, mode)
	}
}

// findNodeIDsByAddress looks up nodes by their slug property in the
// org-scoped node_by_property index. TACK_REPAIR_FIND_ORG_ID is required
// because address lookups are tenant-scoped; there is no global address index.
func findNodeIDsByAddress(ctx context.Context, env *Env, result *repairFindResult) ([]uuid.UUID, error) {
	log := env.Log
	orgID, err := readRequiredUUIDEnv(repairFindOrgIDEnv)
	if err != nil {
		return nil, err
	}
	nodeType, err := readRequiredStringEnv(repairFindNodeTypeEnv)
	if err != nil {
		return nil, err
	}
	address, err := readRepairAddressEnv()
	if err != nil {
		return nil, err
	}
	result.Input[repairFindOrgIDEnv] = orgID.String()
	result.Input[repairFindNodeTypeEnv] = nodeType
	result.Input[repairFindAddressEnv] = address
	addressRaw, marshalErr := json.Marshal(address)
	if marshalErr != nil {
		return nil, fmt.Errorf("marshal address value %q: %w", address, marshalErr)
	}
	matched, err := env.Stores.Nodes.ListByProperty(ctx, orgID, nodeType, "slug", addressRaw)
	if err != nil {
		log.ErrorContext(ctx, "repair.find.address",
			slog.String("org_id", orgID.String()),
			slog.String("node_type", nodeType),
			slog.String("address", address),
			slog.String("err", err.Error()),
		)
		return nil, fmt.Errorf("find %q nodes with slug %q in org %s: %w", nodeType, address, orgID, err)
	}
	nodeIDs := make([]uuid.UUID, 0, len(matched))
	for _, n := range matched {
		nodeIDs = append(nodeIDs, n.ID)
	}
	return nodeIDs, nil
}

// findNodeIDsByProperty looks up nodes by an arbitrary indexed property value.
func findNodeIDsByProperty(ctx context.Context, env *Env, result *repairFindResult) ([]uuid.UUID, error) {
	orgID, err := readRequiredUUIDEnv(repairFindOrgIDEnv)
	if err != nil {
		return nil, err
	}
	nodeType, err := readRequiredStringEnv(repairFindNodeTypeEnv)
	if err != nil {
		return nil, err
	}
	propertyName, err := readRequiredStringEnv(repairFindPropEnv)
	if err != nil {
		return nil, err
	}
	value, err := readRequiredJSONEnv(repairFindValueEnv)
	if err != nil {
		return nil, err
	}
	result.Input[repairFindOrgIDEnv] = orgID.String()
	result.Input[repairFindNodeTypeEnv] = nodeType
	result.Input[repairFindPropEnv] = propertyName
	result.Input[repairFindValueEnv] = string(value)
	nodes, err := env.Stores.Nodes.ListByProperty(ctx, orgID, nodeType, propertyName, value)
	if err != nil {
		return nil, err
	}
	nodeIDs := make([]uuid.UUID, 0, len(nodes))
	for _, currentNode := range nodes {
		nodeIDs = append(nodeIDs, currentNode.ID)
	}
	return nodeIDs, nil
}
