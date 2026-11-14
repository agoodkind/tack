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

// writeAllReferenceKeys reasserts rendered reference keys for an org. It returns
// only claims that were absent or belonged to another node before the write.
// Reasserting a node's existing claim does not require a mutation event.
func writeAllReferenceKeys(
	ctx context.Context,
	env *Env,
	orgID uuid.UUID,
) ([]ReferenceKeyWrite, error) {
	keys, err := enumerateReferenceKeys(ctx, env, orgID, nil)
	if err != nil {
		return nil, err
	}

	claimed, err := unheldReferenceKeys(ctx, env.Stores.Nodes, orgID, keys)
	if err != nil {
		return nil, err
	}

	keysByNode := make(map[uuid.UUID][]node.ReferenceKey)

	for _, key := range keys {
		keysByNode[key.View.ID] = append(keysByNode[key.View.ID], key.Key)
	}

	written, err := writeReferenceKeysByNode(ctx, env.Stores.Nodes, orgID, keysByNode)
	if err != nil {
		// Return exactly the new claims from successful writes for audit recording.
		// Include partial changes without reporting writes that failed (TACK-452).
		return claimedOnWrittenNodes(claimed, written), err
	}

	return claimed, nil
}

// claimedOnWrittenNodes selects claims from nodes with successful writes.
func claimedOnWrittenNodes(
	claimed []ReferenceKeyWrite,
	written map[uuid.UUID]bool,
) []ReferenceKeyWrite {
	applied := make([]ReferenceKeyWrite, 0, len(claimed))
	for _, key := range claimed {
		if written[key.NodeID] {
			applied = append(applied, key)
		}
	}

	return applied
}

type referenceKeyWriter interface {
	SetReferenceKeys(context.Context, uuid.UUID, uuid.UUID, []node.ReferenceKey) error
}

// writeReferenceKeysByNode reports successful writes, including partial work
// before a failure.
func writeReferenceKeysByNode(
	ctx context.Context,
	writer referenceKeyWriter,
	orgID uuid.UUID,
	keysByNode map[uuid.UUID][]node.ReferenceKey,
) (map[uuid.UUID]bool, error) {
	nodeIDs := make([]uuid.UUID, 0, len(keysByNode))
	for nodeID := range keysByNode {
		nodeIDs = append(nodeIDs, nodeID)
	}

	sort.Slice(
		nodeIDs,
		func(i, j int) bool {
			return nodeIDs[i].String() < nodeIDs[j].String()
		},
	)

	written := make(map[uuid.UUID]bool, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		if err := writer.SetReferenceKeys(ctx, orgID, nodeID, keysByNode[nodeID]); err != nil {
			wrapped := fmt.Errorf("write reference keys for node %s: %w", nodeID, err)
			telemetry.L(ctx).WarnContext(
				ctx,
				"repair.reference_uniqueness.keys_write_failed",
				slog.String("node_id", nodeID.String()),
				slog.String("err", wrapped.Error()),
			)

			return written, wrapped
		}

		written[nodeID] = true
	}

	return written, nil
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
		telemetry.L(ctx).Warn(
			"repair.reference_uniqueness.types_failed",
			slog.String("org_id", orgID.String()),
			slog.String("err", wrapped.Error()),
		)

		return nil, wrapped
	}

	typeIndex := node.BuildTypeIndex(nodeTypes)
	allKeys := make([]repairedReferenceKey, 0)
	scopeCache := make(map[uuid.UUID]map[string]string)

	for _, nodeType := range nodeTypes {
		if len(nodeType.ReferenceTemplates) == 0 {
			continue
		}

		views, listErr := env.Stores.Views.List(
			ctx,
			node.NodeListQuery{
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
			},
		)
		if listErr != nil {
			wrapped := fmt.Errorf("list %s nodes in org %s: %w", nodeType.TypeKey, orgID, listErr)
			telemetry.L(ctx).Warn(
				"repair.reference_uniqueness.views_failed",
				slog.String("org_id", orgID.String()),
				slog.String("node_type", nodeType.TypeKey),
				slog.String("err", wrapped.Error()),
			)

			return nil, wrapped
		}

		for _, view := range views {
			keys, keyErr := referenceKeysForRepairedView(
				ctx,
				env,
				orgID,
				typeIndex,
				nodeType,
				view,
				scopeCache,
			)
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
	// Without templates, return nil to preserve existing claims. An empty
	// non-nil slice requests release of all claims for the node.
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
			wrapped := fmt.Errorf(
				"render template %q for node %s: %w",
				template.Name,
				view.ID,
				renderErr,
			)
			telemetry.L(ctx).Warn(
				"repair.reference_uniqueness.render_failed",
				slog.String("node_id", view.ID.String()),
				slog.String("err", wrapped.Error()),
			)

			return nil, wrapped
		}

		keys = append(keys, node.ReferenceKey{TemplateName: template.Name, Encoded: encoded})
	}

	return keys, nil
}
