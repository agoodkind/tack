package ops

import (
	"context"
	"errors"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

func repairTypeScopeIDs(
	ctx context.Context,
	env *Env,
	orgID uuid.UUID,
	nodeType *node.NodeType,
	propertyDefs []*node.PropertyDef,
	typeIndex map[string]*node.NodeType,
	scopeCache map[uuid.UUID]map[string]string,
) (int, int, int) {
	views, err := scopeRepairViews(ctx, env, orgID, nodeType.TypeKey)
	if err != nil {
		return 0, 0, 0
	}

	indexedProps := indexedPropsFor(nodeType, propertyDefs)
	repaired, skipped, refused := 0, 0, 0

	for _, view := range views {
		currentNode, readDigest, err := env.Stores.Nodes.GetForUpdate(ctx, orgID, view.ID)
		if err != nil || currentNode == nil {
			skipped++

			continue
		}

		canonicalScopeID, err := deriveScopeID(ctx, env, currentNode)
		if err != nil {
			env.Log.ErrorContext(
				ctx,
				"repair.scope_ids.parent_failed",
				slog.String("node_id", currentNode.ID.String()),
				slog.String("err", err.Error()),
			)
			refused++

			continue
		}

		if canonicalScopeID == uuid.Nil {
			skipped++

			continue
		}

		if rawUUIDProp(currentNode.Props, "scope_id") == canonicalScopeID {
			skipped++

			continue
		}

		updatedNode, updatedView, oldProps := repairedScopeRecords(currentNode, canonicalScopeID)
		// A scope change changes the rendered reference. Claim the new scope's
		// keys to prevent collisions. Omitting those claims would retain the
		// old claims without reserving the new reference.
		referenceKeys, keyErr := referenceKeysForRepairedView(
			ctx,
			env,
			orgID,
			typeIndex,
			nodeType,
			updatedView,
			scopeCache,
		)
		if keyErr != nil {
			logScopeRepairNodeFailure(
				ctx,
				env,
				"repair.scope_ids: render reference keys",
				currentNode,
				keyErr,
			)
			skipped++

			continue
		}

		if err := env.Stores.Nodes.UpdateAtomicFrom(
			ctx,
			readDigest,
			updatedNode,
			updatedView,
			oldProps,
			indexedProps,
			referenceKeys,
		); err != nil {
			logScopeRepairNodeFailure(ctx, env, "repair.scope_ids: update node", currentNode, err)
			if errors.Is(err, domain.ErrConflict) {
				refused++
			} else {
				skipped++
			}

			continue
		}

		repaired++
	}

	return repaired, skipped, refused
}
