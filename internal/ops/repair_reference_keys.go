package ops

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

type repairedReferenceKey struct {
	View *node.NodeView
	Key  node.ReferenceKey
}

// writeAllReferenceKeys returns the keys it wrote, so the caller can record
// one ledger event per key.
func writeAllReferenceKeys(
	ctx context.Context,
	env *Env,
	orgID uuid.UUID,
) ([]ReferenceKeyWrite, error) {
	keys, err := enumerateReferenceKeys(ctx, env, orgID, nil)
	if err != nil {
		return nil, err
	}
	keysByNode := make(map[uuid.UUID][]node.ReferenceKey)
	for _, key := range keys {
		keysByNode[key.View.ID] = append(keysByNode[key.View.ID], key.Key)
	}
	if err := writeReferenceKeysByNode(ctx, env.Stores.Nodes, orgID, keysByNode); err != nil {
		return nil, err
	}
	written := make([]ReferenceKeyWrite, 0, len(keys))
	for _, key := range keys {
		written = append(written, ReferenceKeyWrite{
			OrgID: orgID, NodeID: key.View.ID, NodeType: key.View.NodeType,
			TemplateName: key.Key.TemplateName, Encoded: key.Key.Encoded,
		})
	}
	return written, nil
}

type referenceKeyWriter interface {
	SetReferenceKeys(context.Context, uuid.UUID, uuid.UUID, []node.ReferenceKey) error
}

func writeReferenceKeysByNode(
	ctx context.Context,
	writer referenceKeyWriter,
	orgID uuid.UUID,
	keysByNode map[uuid.UUID][]node.ReferenceKey,
) error {
	nodeIDs := make([]uuid.UUID, 0, len(keysByNode))
	for nodeID := range keysByNode {
		nodeIDs = append(nodeIDs, nodeID)
	}
	sort.Slice(nodeIDs, func(i, j int) bool {
		return nodeIDs[i].String() < nodeIDs[j].String()
	})
	for _, nodeID := range nodeIDs {
		if err := writer.SetReferenceKeys(ctx, orgID, nodeID, keysByNode[nodeID]); err != nil {
			wrapped := fmt.Errorf("write reference keys for node %s: %w", nodeID, err)
			telemetry.L(ctx).WarnContext(ctx, "repair.reference_uniqueness.keys_write_failed",
				slog.String("node_id", nodeID.String()), slog.String("err", wrapped.Error()))
			return wrapped
		}
	}
	return nil
}

func enumerateReferenceKeys(
	ctx context.Context,
	env *Env,
	orgID uuid.UUID,
	createdBefore *time.Time,
) ([]repairedReferenceKey, error) {
	nodeTypes, err := env.Stores.NodeTypes.List(ctx, orgID)
	if err != nil {
		wrapped := fmt.Errorf("list node types for org %s: %w", orgID, err)
		telemetry.L(ctx).Warn("repair.reference_uniqueness.types_failed",
			slog.String("org_id", orgID.String()), slog.String("err", wrapped.Error()))
		return nil, wrapped
	}
	typeIndex := node.BuildTypeIndex(nodeTypes)
	allKeys := make([]repairedReferenceKey, 0)
	scopeCache := make(map[uuid.UUID]map[string]string)
	for _, nodeType := range nodeTypes {
		if len(nodeType.ReferenceTemplates) == 0 {
			continue
		}
		views, listErr := env.Stores.Views.List(ctx, node.NodeListQuery{
			OrgID:            orgID,
			NodeType:         nodeType.TypeKey,
			ByProperty:       nil,
			BySourceRelation: nil,
			ByTargetRelation: nil,
			CreatedAfter:     nil,
			CreatedBefore:    createdBefore,
			PropFilters:      nil,
			Limit:            0,
			Cursor:           "",
		})
		if listErr != nil {
			wrapped := fmt.Errorf("list %s nodes in org %s: %w", nodeType.TypeKey, orgID, listErr)
			telemetry.L(ctx).Warn("repair.reference_uniqueness.views_failed",
				slog.String("org_id", orgID.String()),
				slog.String("node_type", nodeType.TypeKey),
				slog.String("err", wrapped.Error()),
			)
			return nil, wrapped
		}
		for _, view := range views {
			keys, keyErr := referenceKeysForRepairedView(ctx, env, orgID, typeIndex, nodeType, view, scopeCache)
			if keyErr != nil {
				return nil, keyErr
			}
			for _, key := range keys {
				allKeys = append(allKeys, repairedReferenceKey{View: view, Key: key})
			}
		}
	}
	return allKeys, nil
}

func referenceKeysForRepairedView(
	ctx context.Context,
	env *Env,
	orgID uuid.UUID,
	typeIndex map[string]*node.NodeType,
	nodeType *node.NodeType,
	view *node.NodeView,
	scopeCache map[uuid.UUID]map[string]string,
) ([]node.ReferenceKey, error) {
	// No templates means no keys to claim, which the storage layer reads as
	// leave ownership alone. Returning an empty non-nil slice instead would
	// read as an explicit release and strip claims a node already holds.
	if len(nodeType.ReferenceTemplates) == 0 {
		return nil, nil
	}
	scopeRefs, err := scopeReferencesForView(ctx, env, orgID, typeIndex, view, scopeCache)
	if err != nil {
		return nil, err
	}
	input := node.ReferenceRenderInput{
		NodeTypeKey: nodeType.TypeKey,
		Props:       view.Props,
		ScopeRefs:   scopeRefs,
	}
	keys := make([]node.ReferenceKey, 0, len(nodeType.ReferenceTemplates))
	for _, template := range nodeType.ReferenceTemplates {
		encoded, renderErr := template.Render(input)
		if renderErr != nil {
			wrapped := fmt.Errorf("render template %q for node %s: %w", template.Name, view.ID, renderErr)
			telemetry.L(ctx).Warn("repair.reference_uniqueness.render_failed",
				slog.String("node_id", view.ID.String()), slog.String("err", wrapped.Error()))
			return nil, wrapped
		}
		keys = append(keys, node.ReferenceKey{TemplateName: template.Name, Encoded: encoded})
	}
	return keys, nil
}
