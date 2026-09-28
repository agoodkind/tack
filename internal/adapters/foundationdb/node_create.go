package foundationdb

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// CreateAtomic writes a node and its initial indexes and relationships in one
// transaction. When search work is enabled, the same transaction also
// schedules search work for the node.
func (s *NodeStore) CreateAtomic(ctx context.Context, current *node.Node, view *node.NodeView, relationships []*node.Relationship, indexedProps []string, referenceKeys []node.ReferenceKey, idempotency *node.IdempotencyRecord) (err error) {
	defer telemetry.FDBOp(ctx, "store.node.create_atomic")(&err)
	err = runNodeMutation(ctx, s.db, "create node "+current.ID.String()+" atomically", func(tr fdb.Transaction) error {
		if err := writeCreateIdempotency(ctx, tr, current.OrgID, idempotency); err != nil {
			return err
		}
		if err := writeNodeRecords(ctx, tr, current, view); err != nil {
			return err
		}
		writeCreatePropertyIndexes(tr, current, indexedProps)
		if err := claimReferenceKeys(tr, current.OrgID, current.ID, referenceKeys); err != nil {
			return err
		}
		if err := writeCreateRelationships(ctx, tr, current.OrgID, relationships); err != nil {
			return err
		}
		if s.searchWork {
			if _, err := scheduleSearchChange(ctx, tr, s.clock.Now(), current.OrgID, current.ID, searchChangeContent); err != nil {
				return err
			}
		}
		return writeStagedIntent(ctx, tr)
	})
	if err != nil {
		return err
	}
	commitStagedIntent(ctx)
	return nil
}

func writeCreatePropertyIndexes(tr fdb.Transaction, current *node.Node, indexedProps []string) {
	for _, propName := range indexedProps {
		value, ok := current.Props[propName]
		if ok && len(value) > 0 {
			tr.Set(fdb.Key(nodeByPropertyKey(current.OrgID, current.NodeType, propName, encodePropertyValue(value), current.ID)), []byte{})
		}
	}
}

func writeCreateRelationships(ctx context.Context, tr fdb.Transaction, orgID uuid.UUID, relationships []*node.Relationship) error {
	for _, relationship := range relationships {
		if relationship.OrgID == uuid.Nil {
			relationship.OrgID = orgID
		}
		metadata, err := json.Marshal(relationship)
		if err != nil {
			wrapped := fmt.Errorf("marshal relationship %s %s %s: %w", relationship.SourceID, relationship.RelationType, relationship.TargetID, err)
			telemetry.L(ctx).ErrorContext(ctx, "node.relationship.marshal_failed", slog.String("err", wrapped.Error()), slog.String("source_id", relationship.SourceID.String()), slog.String("target_id", relationship.TargetID.String()))
			return loggedSearchError{err: wrapped}
		}
		tr.Set(fdb.Key(relationshipKey(relationship.OrgID, relationship.SourceID, relationship.RelationType, relationship.TargetID)), metadata)
		tr.Set(fdb.Key(relationshipReverseKey(relationship.OrgID, relationship.TargetID, relationship.RelationType, relationship.SourceID)), []byte{})
	}
	return nil
}

func writeCreateIdempotency(ctx context.Context, tr fdb.Transaction, orgID uuid.UUID, record *node.IdempotencyRecord) error {
	if record == nil {
		return nil
	}
	key := fdb.Key(idempotencyKey(orgID, record.Key))
	existing, err := tr.Get(key).Get()
	if err != nil {
		wrapped := fmt.Errorf("read idempotency key: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "node.idempotency.read_failed", slog.String("err", wrapped.Error()))
		return loggedSearchError{err: wrapped}
	}
	if len(existing) > 0 {
		wrapped := fmt.Errorf("idempotency key already exists: %w", domain.ErrConflict)
		telemetry.L(ctx).ErrorContext(ctx, "node.idempotency.conflict", slog.String("err", wrapped.Error()), slog.String("org_id", orgID.String()))
		return loggedSearchError{err: wrapped}
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		wrapped := fmt.Errorf("marshal idempotency record: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "node.idempotency.marshal_failed", slog.String("err", wrapped.Error()))
		return loggedSearchError{err: wrapped}
	}
	tr.Set(key, encoded)
	return nil
}
