package foundationdb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// UpdateAtomic stores a node and reconciles its indexes and relationships in
// one transaction. When search work is enabled, the same transaction also
// schedules search work for the node and the endpoints of each changed
// relationship. The transaction writes one change event for the node when its
// stored value changes and one for each changed relationship.
func (s *NodeStore) UpdateAtomic(ctx context.Context, current *node.Node, view *node.NodeView, oldProps map[string]json.RawMessage, indexedProps []string, referenceKeys []node.ReferenceKey, relationshipChanges ...node.RelationshipChanges) (err error) {
	defer telemetry.FDBOp(ctx, "store.node.update_atomic")(&err)
	return s.updateAtomic(ctx, nil, current, view, oldProps, indexedProps, referenceKeys, relationshipChanges)
}

// updateAtomic performs the UpdateAtomic write. When readDigest is not nil,
// updateAtomic compares it with the stored record digest inside the write
// transaction. If the digests differ, updateAtomic returns an error that wraps
// domain.ErrConflict and does not write any changes.
func (s *NodeStore) updateAtomic(
	ctx context.Context, readDigest *node.Digest, current *node.Node, view *node.NodeView, oldProps map[string]json.RawMessage,
	indexedProps []string, referenceKeys []node.ReferenceKey, relationshipChanges []node.RelationshipChanges,
) error {
	changes, err := beginMutationAudit(ctx, current.OrgID)
	if err != nil {
		return err
	}
	err = runNodeMutation(ctx, s.db, "update node "+current.ID.String()+" atomically", func(tr fdb.Transaction) error {
		changes.startAttempt()
		source, err := tr.Get(fdb.Key(nodeInstanceKey(current.OrgID, current.NodeType, current.ID))).Get()
		if err != nil {
			return searchReadFailure(ctx, "read node "+current.ID.String(), err)
		}
		if readDigest != nil && node.PrimaryDigest(source) != *readDigest {
			return mutationRefusal(ctx, current.ID, "stored node changed after the command read it; the operator must run the command again to read the current record", domain.ErrConflict)
		}
		strict, err := s.fenceAttempt(ctx, tr, current.OrgID, current.ID)
		if err != nil {
			return err
		}
		encoded, err := writeNodeRecords(ctx, tr, current, view)
		if err != nil {
			return err
		}
		reconcilePropertyIndexes(tr, current, oldProps, indexedProps)
		if referenceKeys != nil {
			if err := writeReferenceKeys(tr, current.OrgID, current.ID, referenceKeys); err != nil {
				return err
			}
		}
		if err := applyRelationshipChanges(ctx, tr, relationshipChanges, changes); err != nil {
			return err
		}
		if err := checkStrictWrite(ctx, tr, strict, current, relationshipChanges); err != nil {
			return err
		}
		if s.searchWork {
			now := s.clock.Now()
			if _, err := scheduleSearchChange(ctx, tr, now, current.OrgID, current.ID, searchChangeContent); err != nil {
				return err
			}
			if err := scheduleRelatedSearchWork(ctx, tr, now, relationshipChanges); err != nil {
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

func reconcilePropertyIndexes(tr fdb.Transaction, current *node.Node, oldProps map[string]json.RawMessage, indexedProps []string) {
	for _, propName := range indexedProps {
		oldValue, newValue := oldProps[propName], current.Props[propName]
		if bytes.Equal(oldValue, newValue) {
			continue
		}
		if len(oldValue) > 0 {
			tr.Clear(fdb.Key(nodeByPropertyKey(current.OrgID, current.NodeType, propName, encodePropertyValue(oldValue), current.ID)))
		}
		if len(newValue) > 0 {
			tr.Set(fdb.Key(nodeByPropertyKey(current.OrgID, current.NodeType, propName, encodePropertyValue(newValue), current.ID)), []byte{})
		}
	}
}

// writeNodeRecords writes the node and resolution records of current inside tr.
// It writes the view record when view is not nil and returns the encoded node record.
func writeNodeRecords(ctx context.Context, tr fdb.Transaction, current *node.Node, view *node.NodeView) ([]byte, error) {
	encodedNode, err := json.Marshal(current)
	if err != nil {
		wrapped := fmt.Errorf("marshal node %s: %w", current.ID, err)
		telemetry.L(ctx).ErrorContext(ctx, "node.marshal_failed", slog.String("err", wrapped.Error()), slog.String("node_id", current.ID.String()))
		return nil, loggedSearchError{err: wrapped}
	}
	tr.Set(fdb.Key(nodeInstanceKey(current.OrgID, current.NodeType, current.ID)), encodedNode)
	encodedResolve, err := json.Marshal(&node.NodeResolve{OrgID: current.OrgID, NodeType: current.NodeType})
	if err != nil {
		wrapped := fmt.Errorf("marshal node resolution %s: %w", current.ID, err)
		telemetry.L(ctx).ErrorContext(ctx, "node.resolve.marshal_failed", slog.String("err", wrapped.Error()), slog.String("node_id", current.ID.String()))
		return nil, loggedSearchError{err: wrapped}
	}
	tr.Set(fdb.Key(nodeResolveKey(current.ID)), encodedResolve)
	if view != nil {
		encodedView, err := json.Marshal(view)
		if err != nil {
			wrapped := fmt.Errorf("marshal node view %s: %w", current.ID, err)
			telemetry.L(ctx).ErrorContext(ctx, "node.view.marshal_failed", slog.String("err", wrapped.Error()), slog.String("node_id", current.ID.String()))
			return nil, loggedSearchError{err: wrapped}
		}
		tr.Set(fdb.Key(nodeViewKey(current.OrgID, current.NodeType, current.ID)), encodedView)
	}
	return encodedNode, nil
}
