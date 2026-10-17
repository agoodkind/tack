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
func checkDefaultChildProps(ctx context.Context, env *Env, orgIDs map[uuid.UUID]struct{}) error {
	var refusals []error
	for orgID := range orgIDs {
		orgRefusals, err := checkOrgDefaultChildProps(ctx, env, orgID)
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

func checkOrgDefaultChildProps(ctx context.Context, env *Env, orgID uuid.UUID) ([]error, error) {
	nodeTypes, err := env.Stores.NodeTypes.List(ctx, orgID)
	if err != nil {
		wrapped := fmt.Errorf("list node types of organization %s for the default children check: %w", orgID, err)
		env.Log.ErrorContext(ctx, "backfill.default_children_check_failed",
			slog.String("org_id", orgID.String()), slog.String("err", wrapped.Error()))
		return nil, wrapped
	}
	definitions, err := env.Stores.PropertyDefs.List(ctx, orgID)
	if err != nil {
		wrapped := fmt.Errorf("list property definitions of organization %s for the default children check: %w", orgID, err)
		env.Log.ErrorContext(ctx, "backfill.default_children_check_failed",
			slog.String("org_id", orgID.String()), slog.String("err", wrapped.Error()))
		return nil, wrapped
	}
	typeIndex := node.BuildTypeIndex(nodeTypes)
	var refusals []error
	for _, nodeType := range nodeTypes {
		for _, defaultChild := range nodeType.DefaultChildren {
			childType := typeIndex[defaultChild.TypeKey]
			if childType == nil {
				continue
			}
			props := defaultChildProps(defaultChild, checkedParentRaw)
			if err := service.ValidateNodeProps(childType, definitions, props); err != nil {
				refusals = append(refusals, fmt.Errorf("default child %q of node type %q in organization %s: %w",
					defaultChild.Name, nodeType.TypeKey, orgID, err))
			}
		}
	}
	return refusals, nil
}

// checkedParentRaw is the JSON encoding of the nil UUID string. The property
// name check uses it as the parent value for every default child.
var checkedParentRaw = json.RawMessage(strconv.Quote(uuid.Nil.String()))

// defaultChildProps copies dc.Props, sets parent_id and scope_id to parentRaw
// in the copy, and returns the copy.
func defaultChildProps(dc node.DefaultChild, parentRaw json.RawMessage) map[string]json.RawMessage {
	props := make(map[string]json.RawMessage, len(dc.Props)+2)
	maps.Copy(props, dc.Props)
	props["parent_id"] = parentRaw
	props["scope_id"] = parentRaw
	return props
}
