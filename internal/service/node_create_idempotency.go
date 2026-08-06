package service

import (
	"context"
	"errors"
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

func (s *NodeService) createNodeAtomic(
	ctx context.Context,
	log *slog.Logger,
	in CreateInput,
	n *node.Node,
	view *node.NodeView,
	relationships []*node.Relationship,
	indexedProps []string,
	referenceKeys []node.ReferenceKey,
	now time.Time,
) (*CreateResult, error) {
	idempotencyRecord := createIdempotencyRecord(in, n.ID, now)
	err := s.nodes.CreateAtomic(ctx, n, view, relationships, indexedProps, referenceKeys, idempotencyRecord)
	if err == nil {
		return nil, nil
	}
	if errors.Is(err, domain.ErrConflict) && in.IdempotencyKey != "" {
		existingResult, lookupErr := s.lookupCreateAfterConflict(ctx, log, n.OrgID, in)
		if lookupErr != nil {
			return nil, lookupErr
		}
		if existingResult != nil {
			return existingResult, nil
		}
	}
	return nil, logCreateAtomicError(ctx, log, n, err)
}

func (s *NodeService) lookupCreateAfterConflict(
	ctx context.Context,
	log *slog.Logger,
	orgID uuid.UUID,
	in CreateInput,
) (*CreateResult, error) {
	existing, lookupErr := s.nodes.LookupIdempotencyKey(ctx, orgID, in.IdempotencyKey)
	if lookupErr != nil {
		log.ErrorContext(ctx, "node.Create: lookup idempotency after conflict", slog.String("err", lookupErr.Error()))
		return nil, fmt.Errorf("lookup idempotency after create conflict: %w", lookupErr)
	}
	if existing == nil || idempotencyFingerprintConflict(existing.Fingerprint, in.IdempotencyFingerprint) {
		return nil, nil
	}
	existingView, getErr := s.reader.Get(ctx, existing.NodeID)
	if getErr != nil {
		log.ErrorContext(ctx, "node.Create: get existing idempotent node after conflict",
			slog.String("node_id", existing.NodeID.String()),
			slog.String("err", getErr.Error()),
		)
		return nil, fmt.Errorf("get existing idempotent node after conflict: %w", getErr)
	}
	if existingView == nil {
		return nil, nil
	}
	return &CreateResult{View: existingView, Existed: true}, nil
}

func logCreateAtomicError(ctx context.Context, log *slog.Logger, n *node.Node, err error) error {
	log.ErrorContext(ctx, "node.Create: atomic write",
		slog.String("node_type", n.NodeType),
		slog.String("node_id", n.ID.String()),
		slog.String("err", err.Error()),
	)
	return fmt.Errorf("create node: %w", err)
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
