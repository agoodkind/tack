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
	changes, err := beginMutationAudit(ctx, current.OrgID)
	if err != nil {
		return err
	}
	for _, relationship := range relationships {
		if relationship.OrgID == uuid.Nil {
			relationship.OrgID = current.OrgID
		}
	}
	err = runNodeMutation(ctx, s.db, "create node "+current.ID.String()+" atomically", func(tr fdb.Transaction) error {
		changes.startAttempt()
		strict, err := s.fenceAttempt(ctx, tr, current.OrgID, current.ID)
		if err != nil {
			return err
		}
		if err := writeCreateIdempotency(ctx, tr, current.OrgID, idempotency); err != nil {
			return err
		}
		source, err := tr.Get(fdb.Key(nodeInstanceKey(current.OrgID, current.NodeType, current.ID))).Get()
		if err != nil {
			return searchReadFailure(ctx, "read node "+current.ID.String(), err)
		}
		encoded, err := writeNodeRecords(ctx, tr, current, view)
		if err != nil {
			return err
		}
		writeCreatePropertyIndexes(tr, current, indexedProps)
		if err := claimReferenceKeys(tr, current.OrgID, current.ID, referenceKeys); err != nil {
			return err
		}
		if err := addRelationships(ctx, tr, relationships, changes); err != nil {
			return err
		}
		if err := checkStrictWrite(ctx, tr, strict, current, []node.RelationshipChanges{{Add: relationships, Remove: nil}}); err != nil {
			return err
		}
		if s.searchWork {
			if _, err := scheduleSearchChange(ctx, tr, s.clock.Now(), current.OrgID, current.ID, searchChangeContent); err != nil {
				return err
			}
		}
		if err := changes.record(nodeTarget(current), source, encoded); err != nil {
			return nodeOperationFailure(ctx, "record the change of node "+current.ID.String(), err)
		}
		return changes.append(ctx, tr)
	})
	if err != nil {
		return err
	}
	changes.finish(ctx)
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
