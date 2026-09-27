package foundationdb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// UpdateAtomic stores a node and reconciles its indexes and relationships in
// one transaction. When search work is enabled, the same transaction also
// schedules search work for the node and the endpoints of each changed
// relationship.
func (s *NodeStore) UpdateAtomic(ctx context.Context, current *node.Node, view *node.NodeView, oldProps map[string]json.RawMessage, indexedProps []string, referenceKeys []node.ReferenceKey, relationshipChanges ...node.RelationshipChanges) (err error) {
	defer telemetry.FDBOp(ctx, "store.node.update_atomic")(&err)
	err = runNodeMutation(ctx, s.db, "node.update_atomic", func(tr fdb.Transaction) error {
		if err := writeNodeRecords(ctx, tr, current, view); err != nil {
			return err
		}
		reconcilePropertyIndexes(tr, current, oldProps, indexedProps)
		if referenceKeys != nil {
			if err := writeReferenceKeys(tr, current.OrgID, current.ID, referenceKeys); err != nil {
				return err
			}
		}
		if err := applyRelationshipChanges(ctx, tr, relationshipChanges); err != nil {
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
		return writeStagedIntent(ctx, tr)
	})
	if err != nil {
		if searchFailureWasLogged(err) {
			return err
		}
		wrapped := fmt.Errorf("update node %s atomically: %w", current.ID, err)
		if !searchFailureWasLogged(err) {
			telemetry.L(ctx).ErrorContext(ctx, "node.update_atomic_failed", slog.String("err", wrapped.Error()), slog.String("node_id", current.ID.String()))
		}
		return wrapped
	}
	commitStagedIntent(ctx)
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

func applyRelationshipChanges(ctx context.Context, tr fdb.Transaction, changesList []node.RelationshipChanges) error {
	for _, changes := range changesList {
		for _, relationship := range changes.Remove {
			tr.Clear(fdb.Key(relationshipKey(relationship.OrgID, relationship.SourceID, relationship.RelationType, relationship.TargetID)))
			tr.Clear(fdb.Key(relationshipReverseKey(relationship.OrgID, relationship.TargetID, relationship.RelationType, relationship.SourceID)))
		}
		for _, relationship := range changes.Add {
			metadata, err := json.Marshal(relationship)
			if err != nil {
				wrapped := fmt.Errorf("marshal relationship %s %s %s: %w", relationship.SourceID, relationship.RelationType, relationship.TargetID, err)
				telemetry.L(ctx).ErrorContext(ctx, "node.relationship.marshal_failed", slog.String("err", wrapped.Error()), slog.String("source_id", relationship.SourceID.String()), slog.String("target_id", relationship.TargetID.String()))
				return loggedSearchError{err: wrapped}
			}
			tr.Set(fdb.Key(relationshipKey(relationship.OrgID, relationship.SourceID, relationship.RelationType, relationship.TargetID)), metadata)
			tr.Set(fdb.Key(relationshipReverseKey(relationship.OrgID, relationship.TargetID, relationship.RelationType, relationship.SourceID)), []byte{})
		}
	}
	return nil
}

func writeNodeRecords(ctx context.Context, tr fdb.Transaction, current *node.Node, view *node.NodeView) error {
	encodedNode, err := json.Marshal(current)
	if err != nil {
		wrapped := fmt.Errorf("marshal node %s: %w", current.ID, err)
		telemetry.L(ctx).ErrorContext(ctx, "node.marshal_failed", slog.String("err", wrapped.Error()), slog.String("node_id", current.ID.String()))
		return loggedSearchError{err: wrapped}
	}
	tr.Set(fdb.Key(nodeInstanceKey(current.OrgID, current.NodeType, current.ID)), encodedNode)
	encodedResolve, err := json.Marshal(&node.NodeResolve{OrgID: current.OrgID, NodeType: current.NodeType})
	if err != nil {
		wrapped := fmt.Errorf("marshal node resolution %s: %w", current.ID, err)
		telemetry.L(ctx).ErrorContext(ctx, "node.resolve.marshal_failed", slog.String("err", wrapped.Error()), slog.String("node_id", current.ID.String()))
		return loggedSearchError{err: wrapped}
	}
	tr.Set(fdb.Key(nodeResolveKey(current.ID)), encodedResolve)
	if view != nil {
		encodedView, err := json.Marshal(view)
		if err != nil {
			wrapped := fmt.Errorf("marshal node view %s: %w", current.ID, err)
			telemetry.L(ctx).ErrorContext(ctx, "node.view.marshal_failed", slog.String("err", wrapped.Error()), slog.String("node_id", current.ID.String()))
			return loggedSearchError{err: wrapped}
		}
		tr.Set(fdb.Key(nodeViewKey(current.OrgID, current.NodeType, current.ID)), encodedView)
	}
	return nil
}
