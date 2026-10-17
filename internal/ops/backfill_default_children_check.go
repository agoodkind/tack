package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strconv"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

// checkDefaultChildProps validates the properties of every default child in
// every organization with the service create validator. It returns one error
// that lists every refused default child.
func checkDefaultChildProps(ctx context.Context, env *Env, orgTypes map[uuid.UUID][]*node.NodeType) error {
	var refusals []error
	for orgID, nodeTypes := range orgTypes {
		orgRefusals, err := checkOrgDefaultChildProps(ctx, env, orgID, nodeTypes)
		if err != nil {
			return err
		}
		refusals = append(refusals, orgRefusals...)
	}
	if len(refusals) == 0 {
		return nil
	}
	refused := fmt.Errorf("refuse the default children backfill: %w", errors.Join(refusals...))
	env.Log.ErrorContext(ctx, "backfill.default_children_refused",
		slog.Int("refused_children", len(refusals)), slog.String("err", refused.Error()))
	return refused
}

// listOrgNodeTypes attempts to read node types for every organization in orgIDs,
// even when a read fails. It returns the node types by organization ID if every
// read succeeds. If any read fails, it logs one
// backfill.default_children_check_failed event, returns one error that joins
// every failed read, and does not return node types.
func listOrgNodeTypes(ctx context.Context, env *Env, orgIDs map[uuid.UUID]struct{}) (map[uuid.UUID][]*node.NodeType, error) {
	orgTypes := make(map[uuid.UUID][]*node.NodeType, len(orgIDs))
	var failures []error
	for orgID := range orgIDs {
		nodeTypes, err := env.Stores.NodeTypes.List(ctx, orgID)
		if err != nil {
			failures = append(failures, fmt.Errorf("list node types of organization %s: %w", orgID, err))
			continue
		}
		orgTypes[orgID] = nodeTypes
	}
	if len(failures) == 0 {
		return orgTypes, nil
	}
	failed := fmt.Errorf("refuse the default children backfill after %d failed node type reads: %w", len(failures), errors.Join(failures...))
	env.Log.ErrorContext(ctx, "backfill.default_children_check_failed",
		slog.Int("failed_reads", len(failures)), slog.String("err", failed.Error()))
	return nil, failed
}

func checkOrgDefaultChildProps(ctx context.Context, env *Env, orgID uuid.UUID, nodeTypes []*node.NodeType) ([]error, error) {
	definitions, err := env.Stores.PropertyDefs.List(ctx, orgID)
	if err != nil {
		wrapped := fmt.Errorf("list property definitions of organization %s for the default children check: %w", orgID, err)
		env.Log.ErrorContext(ctx, "backfill.default_children_check_failed",
			slog.String("org_id", orgID.String()), slog.String("err", wrapped.Error()))
		return nil, wrapped
	}
	typeIndex := node.BuildTypeIndex(nodeTypes)
	schema := node.Schema{OrgID: orgID, Types: typeIndex, Definitions: definitions}
	var refusals []error
	for _, nodeType := range nodeTypes {
		for _, defaultChild := range nodeType.DefaultChildren {
			childType := typeIndex[defaultChild.TypeKey]
			if childType == nil {
				continue
			}
			props := defaultChildProps(defaultChild, checkedParentRaw)
			if err := service.ValidateNodeProps(ctx, schema, childType, "create", props); err != nil {
				refusals = append(refusals, fmt.Errorf("default child %q of node type %q in organization %s: %w",
					defaultChild.Name, nodeType.TypeKey, orgID, err))
			}
		}
	}
	return refusals, nil
}

// checkedParentRaw is the JSON encoding of the nil UUID string.
// checkOrgDefaultChildProps passes it to defaultChildProps for each default
// child's parent_id and scope_id values. checkOrgDefaultChildProps passes
// the complete property map to ValidateNodeProps, which validates every
// property name in that map.
var checkedParentRaw = json.RawMessage(strconv.Quote(uuid.Nil.String()))

func defaultChildProps(dc node.DefaultChild, parentRaw json.RawMessage) map[string]json.RawMessage {
	props := make(map[string]json.RawMessage, len(dc.Props)+2)
	maps.Copy(props, dc.Props)
	props["parent_id"] = parentRaw
	props["scope_id"] = parentRaw
	return props
}
