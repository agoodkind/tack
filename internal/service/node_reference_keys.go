package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

const maxScopeWalkDepth = 32

var (
	errScopeMultipleParents = errors.New("has multiple parents")
	errScopeDepthExceeded   = errors.New("scope walk exceeded ancestor limit")
)

func (s *NodeService) scopeRefsFor(
	ctx context.Context,
	orgID uuid.UUID,
	scopeID uuid.UUID,
) (map[string]string, error) {
	scopeRefs := make(map[string]string)
	if scopeID == uuid.Nil {
		return scopeRefs, nil
	}

	nodeTypes, err := s.nodeTypes.List(ctx, orgID)
	if err != nil {
		slog.ErrorContext(ctx, "node.reference.list_types_failed", slog.String("err", err.Error()))

		return nil, fmt.Errorf("list node types for scope references: %w", err)
	}

	typeIndex := node.BuildTypeIndex(nodeTypes)

	currentID := scopeID
	for depth := 0; currentID != uuid.Nil && depth < maxScopeWalkDepth; depth++ {
		current, getErr := s.reader.Get(ctx, currentID)
		if getErr != nil {
			slog.ErrorContext(
				ctx,
				"node.reference.get_scope_failed",
				slog.String("err", getErr.Error()),
			)

			return nil, fmt.Errorf("get scope node %s: %w", currentID, getErr)
		}

		if current == nil {
			currentID = uuid.Nil

			break
		}

		if nodeType := typeIndex[current.NodeType]; nodeType != nil {
			reference, referenceErr := directScopeReference(ctx, current, nodeType)
			if referenceErr != nil {
				return nil, referenceErr
			}

			for _, feature := range nodeType.Features {
				if reference != "" && scopeRefs[feature] == "" {
					scopeRefs[feature] = reference
				}
			}
		}

		parentID, parentErr := s.scopeParentID(ctx, orgID, currentID)
		if parentErr != nil {
			return nil, parentErr
		}

		currentID = parentID
	}

	if currentID != uuid.Nil {
		err := fmt.Errorf(
			"scope walk from %s exceeded %d ancestors: %w",
			scopeID,
			maxScopeWalkDepth,
			errScopeDepthExceeded,
		)
		slog.ErrorContext(
			ctx,
			"node.reference.scope_depth_exceeded",
			slog.String("err", err.Error()),
		)

		return nil, err
	}

	return scopeRefs, nil
}

func directScopeReference(
	ctx context.Context,
	current *node.NodeView,
	nodeType *node.NodeType,
) (string, error) {
	propertyName := nodeType.Reference.DirectAddressProperty()
	if propertyName == "" {
		return "", nil
	}

	var reference string
	if err := json.Unmarshal(current.Props[propertyName], &reference); err != nil {
		slog.ErrorContext(ctx, "node.reference.decode_failed", slog.String("err", err.Error()))

		return "", fmt.Errorf(
			"decode reference property %q for node %s: %w",
			propertyName,
			current.ID,
			err,
		)
	}

	return reference, nil
}
