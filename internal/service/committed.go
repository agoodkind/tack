package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

func (s *NodeService) readUpdateNode(
	ctx context.Context,
	nodeID uuid.UUID,
) (*node.NodeView, error) {
	existing, err := s.reader.Get(ctx, nodeID)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"node.update_read_failed",
			slog.String("node_id", nodeID.String()),
			slog.String("err", err.Error()),
		)

		return nil, fmt.Errorf("read node %s for its update: %w", nodeID, err)
	}

	if existing == nil {
		return nil, domain.ErrNotFound
	}

	return existing, nil
}

// committedView reads the view of nodeID after a mutation commits.
func (s *NodeService) committedView(ctx context.Context, nodeID uuid.UUID) (*node.NodeView, error) {
	view, err := s.reader.Get(ctx, nodeID)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"node.view_read_failed",
			slog.String("err", err.Error()),
			slog.String("node_id", nodeID.String()),
		)

		return nil, fmt.Errorf("read node %s after its mutation: %w", nodeID, err)
	}

	if view == nil {
		return nil, fmt.Errorf("read node %s after its mutation: %w", nodeID, domain.ErrNotFound)
	}

	return view, nil
}

// mergedUpdateProps returns the properties after applying patch to
// stored. A JSON null in patch removes the property.
func mergedUpdateProps(
	stored map[string]json.RawMessage,
	patch map[string]json.RawMessage,
) map[string]json.RawMessage {
	merged := make(map[string]json.RawMessage, len(stored)+len(patch))
	maps.Copy(merged, stored)

	for name, value := range patch {
		if isDeletedPropValue(value) {
			delete(merged, name)

			continue
		}

		merged[name] = value
	}

	return merged
}

func stampRelationshipChanges(
	changes node.RelationshipChanges,
	actorID uuid.UUID,
	changedAt time.Time,
) node.RelationshipChanges {
	for _, relationship := range changes.Add {
		if relationship.CreatedBy == uuid.Nil {
			relationship.CreatedBy = actorID
		}

		if relationship.CreatedAt.IsZero() {
			relationship.CreatedAt = changedAt
		}
	}

	return changes
}
