package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

const mcpIdempotencyWindow = 24 * time.Hour

func (s *NodeService) lookupExistingCreate(
	ctx context.Context,
	log *slog.Logger,
	orgID uuid.UUID,
	in CreateInput,
) (*CreateResult, error) {
	if in.IdempotencyKey == "" {
		return nil, nil
	}
	existingRecord, err := s.nodes.LookupIdempotencyKey(ctx, orgID, in.IdempotencyKey)
	if err != nil {
		log.ErrorContext(ctx, "node.Create: lookup idempotency", slog.String("err", err.Error()))
		return nil, fmt.Errorf("lookup idempotency key: %w", err)
	}
	if existingRecord == nil || idempotencyRecordExpired(existingRecord) {
		return nil, nil
	}
	if idempotencyFingerprintConflict(existingRecord.Fingerprint, in.IdempotencyFingerprint) {
		log.WarnContext(ctx, "node.Create: idempotency conflict")
		return nil, fmt.Errorf(
			"idempotency key %q reused with different payload: %w",
			in.IdempotencyKey,
			domain.ErrConflict,
		)
	}
	view, err := s.reader.Get(ctx, existingRecord.NodeID)
	if err != nil {
		log.ErrorContext(ctx, "node.Create: get existing idempotent node",
			slog.String("node_id", existingRecord.NodeID.String()),
			slog.String("err", err.Error()),
		)
		return nil, fmt.Errorf("get existing idempotent node: %w", err)
	}
	if view == nil {
		return nil, nil
	}
	return &CreateResult{View: view, Existed: true}, nil
}

func createIdempotencyRecord(in CreateInput, nodeID uuid.UUID, createdAt time.Time) *node.IdempotencyRecord {
	if in.IdempotencyKey == "" {
		return nil
	}
	return &node.IdempotencyRecord{
		Key:         in.IdempotencyKey,
		NodeID:      nodeID,
		Fingerprint: in.IdempotencyFingerprint,
		CreatedAt:   createdAt,
		Source:      in.IdempotencySource,
	}
}

func idempotencyFingerprintConflict(existing, incoming string) bool {
	return existing != "" && incoming != "" && existing != incoming
}

func idempotencyRecordExpired(record *node.IdempotencyRecord) bool {
	if record.Source != "mcp" || record.CreatedAt.IsZero() {
		return false
	}
	return clock.Since(record.CreatedAt) > mcpIdempotencyWindow
}
