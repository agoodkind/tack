package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strconv"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func renumberDuplicateGroup(
	ctx context.Context,
	env *Env,
	duplicate DuplicateReference,
	opts RepairReferenceOptions,
) ([]ReferenceRename, error) {
	ordered := append([]uuid.UUID(nil), duplicate.NodeIDs...)
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].String() < ordered[j].String()
	})
	// UUIDv7 sorts by creation time, so ascending order is oldest first. The
	// retained node is selected by index rather than by rotating the slice: a
	// rotation of three or more nodes would retain the second-oldest instead
	// of the newest.
	retainIndex := 0
	if opts.Keep == keepNewest {
		retainIndex = len(ordered) - 1
	}
	renamed := make([]ReferenceRename, 0, len(ordered)-1)
	for index, nodeID := range ordered {
		if index == retainIndex {
			continue
		}
		rename, err := renumberOneNode(ctx, env, duplicate, nodeID, opts.Execute)
		if err != nil {
			return nil, err
		}
		renamed = append(renamed, rename)
	}
	return renamed, nil
}

func renumberOneNode(
	ctx context.Context,
	env *Env,
	duplicate DuplicateReference,
	nodeID uuid.UUID,
	execute bool,
) (ReferenceRename, error) {
	view, err := env.Stores.Views.Get(ctx, nodeID)
	if err != nil {
		wrapped := fmt.Errorf("get node %s for renumber: %w", nodeID, err)
		env.Log.WarnContext(ctx, "repair.reference_uniqueness.view_get_failed",
			slog.String("node_id", nodeID.String()), slog.String("err", wrapped.Error()))
		return ReferenceRename{}, wrapped
	}
	if view == nil {
		return ReferenceRename{}, fmt.Errorf("get node %s for renumber: not found", nodeID)
	}
	nodeType, template, typeIndex, typeErr := repairReferenceTemplate(ctx, env, duplicate.OrgID, view)
	if typeErr != nil {
		return ReferenceRename{}, typeErr
	}
	scopeRefs, err := scopeReferencesForView(ctx, env, duplicate.OrgID, typeIndex, view, map[uuid.UUID]map[string]string{})
	if err != nil {
		return ReferenceRename{}, err
	}
	counterKey, err := template.CounterKey(node.ReferenceRenderInput{
		NodeTypeKey: nodeType.TypeKey,
		Props:       view.Props,
		ScopeRefs:   scopeRefs,
	})
	if err != nil {
		wrapped := fmt.Errorf("derive counter key for node %s: %w", nodeID, err)
		env.Log.WarnContext(ctx, "repair.reference_uniqueness.counter_key_failed",
			slog.String("node_id", nodeID.String()), slog.String("err", wrapped.Error()))
		return ReferenceRename{}, wrapped
	}
	if !execute {
		return ReferenceRename{
			OrgID:  duplicate.OrgID,
			NodeID: nodeID,
			From:   duplicate.Encoded,
			To:     "(next value on " + counterKey + ")",
		}, nil
	}
	nextValue, err := env.Stores.Nodes.AllocateSequenceByKey(ctx, duplicate.OrgID, counterKey)
	if err != nil {
		wrapped := fmt.Errorf("allocate replacement value for node %s: %w", nodeID, err)
		env.Log.WarnContext(ctx, "repair.reference_uniqueness.allocate_failed",
			slog.String("node_id", nodeID.String()), slog.String("err", wrapped.Error()))
		return ReferenceRename{}, wrapped
	}
	updated, err := env.Stores.Nodes.Get(ctx, duplicate.OrgID, nodeID)
	if err != nil {
		wrapped := fmt.Errorf("read node %s for renumber: %w", nodeID, err)
		env.Log.WarnContext(ctx, "repair.reference_uniqueness.node_get_failed",
			slog.String("node_id", nodeID.String()), slog.String("err", wrapped.Error()))
		return ReferenceRename{}, wrapped
	}
	if updated == nil {
		return ReferenceRename{}, fmt.Errorf("read node %s for renumber: not found", nodeID)
	}
	oldProps := cloneProps(updated.Props)
	updated.Props[template.Generated] = json.RawMessage(strconv.FormatInt(nextValue, 10))
	rendered, err := template.Render(node.ReferenceRenderInput{
		NodeTypeKey: nodeType.TypeKey,
		Props:       updated.Props,
		ScopeRefs:   scopeRefs,
	})
	if err != nil {
		wrapped := fmt.Errorf("render replacement reference for node %s: %w", nodeID, err)
		env.Log.WarnContext(ctx, "repair.reference_uniqueness.render_failed",
			slog.String("node_id", nodeID.String()), slog.String("err", wrapped.Error()))
		return ReferenceRename{}, wrapped
	}
	propertyDefs, err := env.Stores.PropertyDefs.List(ctx, duplicate.OrgID)
	if err != nil {
		wrapped := fmt.Errorf("list property definitions for org %s: %w", duplicate.OrgID, err)
		env.Log.WarnContext(ctx, "repair.reference_uniqueness.property_defs_failed",
			slog.String("org_id", duplicate.OrgID.String()), slog.String("err", wrapped.Error()))
		return ReferenceRename{}, wrapped
	}
	updatedView := viewFromRepairedNode(updated)
	referenceKeys := []node.ReferenceKey{{TemplateName: template.Name, Encoded: rendered}}
	if err := env.Stores.Nodes.UpdateAtomic(
		ctx, updated, updatedView, oldProps, indexedPropsFor(nodeType, propertyDefs), referenceKeys,
	); err != nil {
		wrapped := fmt.Errorf("write renumbered node %s: %w", nodeID, err)
		env.Log.WarnContext(ctx, "repair.reference_uniqueness.update_failed",
			slog.String("node_id", nodeID.String()), slog.String("err", wrapped.Error()))
		return ReferenceRename{}, wrapped
	}
	return ReferenceRename{
		OrgID: duplicate.OrgID, NodeID: nodeID, From: duplicate.Encoded, To: rendered,
	}, nil
}

func repairReferenceTemplate(
	ctx context.Context,
	env *Env,
	orgID uuid.UUID,
	view *node.NodeView,
) (*node.NodeType, *node.ReferenceTemplate, map[string]*node.NodeType, error) {
	nodeTypes, err := env.Stores.NodeTypes.List(ctx, orgID)
	if err != nil {
		wrapped := fmt.Errorf("list node types for org %s: %w", orgID, err)
		env.Log.WarnContext(ctx, "repair.reference_uniqueness.types_failed",
			slog.String("org_id", orgID.String()), slog.String("err", wrapped.Error()))
		return nil, nil, nil, wrapped
	}
	typeIndex := node.BuildTypeIndex(nodeTypes)
	nodeType := typeIndex[view.NodeType]
	if nodeType == nil {
		return nil, nil, nil, fmt.Errorf("node %s has unknown type %q", view.ID, view.NodeType)
	}
	template := nodeType.PrimaryReferenceTemplate()
	if template == nil || template.Generated == "" {
		return nil, nil, nil, fmt.Errorf(
			"node type %q has no generated primary reference for node %s", nodeType.TypeKey, view.ID,
		)
	}
	return nodeType, template, typeIndex, nil
}

func viewFromRepairedNode(currentNode *node.Node) *node.NodeView {
	return &node.NodeView{
		ID:        currentNode.ID,
		OrgID:     currentNode.OrgID,
		NodeType:  currentNode.NodeType,
		Name:      currentNode.Name,
		Props:     cloneProps(currentNode.Props),
		CreatedBy: currentNode.CreatedBy,
		UpdatedBy: currentNode.UpdatedBy,
		CreatedAt: currentNode.CreatedAt,
		UpdatedAt: currentNode.UpdatedAt,
	}
}
