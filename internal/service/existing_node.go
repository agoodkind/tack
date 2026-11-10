package service

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

func (s *NodeService) readNodeForUpdate(
	ctx context.Context,
	nodeID uuid.UUID,
) (*node.NodeView, error) {
	existing, err := s.reader.Get(ctx, nodeID)
	if err != nil {
		log := telemetry.L(ctx)
		log.ErrorContext(
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
