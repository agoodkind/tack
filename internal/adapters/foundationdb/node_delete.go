package foundationdb

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// Delete removes a node and its indexes and relationships in one
// transaction. When search work is enabled, the same transaction also
// schedules retirement of the node's pages. It records each node at the
// other end of a removed relationship with one blind write and no search
// read, and it schedules one access work item for the deleted node. That
// work item schedules access work for at most 100 recorded nodes per slice.
func (s *NodeStore) Delete(ctx context.Context, orgID, nodeID uuid.UUID) (err error) {
	defer telemetry.FDBOp(ctx, "store.node.delete")(&err)
	current, err := s.Get(ctx, orgID, nodeID)
	if err != nil || current == nil {
		return err
	}
	transactionErr := runNodeMutation(ctx, s.db, "node.delete", func(tr fdb.Transaction) error {
		if err := s.clearNode(ctx, tr, current); err != nil {
			return err
		}
		return writeStagedIntent(ctx, tr)
	})
	if transactionErr != nil {
		if searchFailureWasLogged(transactionErr) {
			return transactionErr
		}
		err = fmt.Errorf("delete node %s: %w", nodeID, transactionErr)
		telemetry.L(ctx).ErrorContext(ctx, "node.delete_failed", slog.String("err", err.Error()), slog.String("node_id", nodeID.String()))
		return err
	}
	commitStagedIntent(ctx)
	return nil
}

// clearNode clears the records, indexes, references, and relationships of
// current inside tr. When search work is enabled, it also schedules the
// retirement of the node's pages and access work for each node at the other
// end of a cleared relationship.
func (s *NodeStore) clearNode(ctx context.Context, tr fdb.Transaction, current *node.Node) error {
	orgID, nodeID := current.OrgID, current.ID
	var deletion searchWorkRecord
	if s.searchWork {
		var err error
		deletion, err = scheduleSearchChange(ctx, tr, s.clock.Now(), orgID, nodeID, searchChangeDeletion)
		if err != nil {
			return err
		}
	}
	tr.Clear(fdb.Key(nodeInstanceKey(orgID, current.NodeType, nodeID)))
	tr.Clear(fdb.Key(nodeViewKey(orgID, current.NodeType, nodeID)))
	tr.Clear(fdb.Key(nodeResolveKey(nodeID)))
	sources, err := clearSourceRelationships(ctx, tr, orgID, nodeID)
	if err != nil {
		return err
	}
	targets, err := clearTargetRelationships(ctx, tr, orgID, nodeID)
	if err != nil {
		return err
	}
	if s.searchWork {
		if err := scheduleDeletedFanout(ctx, tr, deletion, append(sources, targets...)); err != nil {
			return err
		}
	}
	for propName, value := range current.Props {
		tr.Clear(fdb.Key(nodeByPropertyKey(orgID, current.NodeType, propName, encodePropertyValue(value), nodeID)))
	}
	return clearReferenceKeys(tr, orgID, nodeID)
}

// clearSourceRelationships clears every relationship from nodeID and returns
// the target of each cleared relationship.
func clearSourceRelationships(ctx context.Context, tr fdb.Transaction, orgID, nodeID uuid.UUID) ([]uuid.UUID, error) {
	rangeValue, err := fdb.PrefixRange(relationshipPrefixBySource(orgID, nodeID, ""))
	if err != nil {
		return nil, relationshipRangeFailure(ctx, "source", nodeID, err)
	}
	items, err := tr.GetRange(rangeValue, fdb.RangeOptions{}).GetSliceWithError()
	if err != nil {
		return nil, searchReadFailure(ctx, "read source relationships for node "+nodeID.String(), err)
	}
	targets := make([]uuid.UUID, 0, len(items))
	for _, item := range items {
		values, unpackErr := tuple.Unpack(stripPrefix(item.Key))
		if unpackErr != nil || len(values) < 5 {
			continue
		}
		relationType, _ := values[3].(string)
		targetText, _ := values[4].(string)
		targetID, parseErr := uuid.Parse(targetText)
		if parseErr == nil {
			tr.Clear(fdb.Key(relationshipReverseKey(orgID, targetID, relationType, nodeID)))
			targets = append(targets, targetID)
		}
	}
	tr.ClearRange(rangeValue)
	return targets, nil
}

// clearTargetRelationships clears every relationship to nodeID and returns
// the source of each cleared relationship.
func clearTargetRelationships(ctx context.Context, tr fdb.Transaction, orgID, nodeID uuid.UUID) ([]uuid.UUID, error) {
	rangeValue, err := fdb.PrefixRange(relationshipReversePrefixByTarget(orgID, nodeID, ""))
	if err != nil {
		return nil, relationshipRangeFailure(ctx, "target", nodeID, err)
	}
	items, err := tr.GetRange(rangeValue, fdb.RangeOptions{}).GetSliceWithError()
	if err != nil {
		return nil, searchReadFailure(ctx, "read target relationships for node "+nodeID.String(), err)
	}
	sources := make([]uuid.UUID, 0, len(items))
	for _, item := range items {
		values, unpackErr := tuple.Unpack(stripPrefix(item.Key))
		if unpackErr != nil || len(values) < 5 {
			continue
		}
		relationType, _ := values[3].(string)
		sourceText, _ := values[4].(string)
		sourceID, parseErr := uuid.Parse(sourceText)
		if parseErr == nil {
			tr.Clear(fdb.Key(relationshipKey(orgID, sourceID, relationType, nodeID)))
			sources = append(sources, sourceID)
		}
	}
	tr.ClearRange(rangeValue)
	return sources, nil
}

func relationshipRangeFailure(ctx context.Context, direction string, nodeID uuid.UUID, err error) error {
	wrapped := fmt.Errorf("create %s relationship range for node %s: %w", direction, nodeID, err)
	telemetry.L(ctx).ErrorContext(ctx, "node.relationship.range_failed", slog.String("err", wrapped.Error()), slog.String("node_id", nodeID.String()))
	return loggedSearchError{err: wrapped}
}
