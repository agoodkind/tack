package foundationdb

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// Delete removes a node and its indexes and relationships in one
// transaction.
//
// When search work is enabled, the transaction schedules retirement of the
// node's pages and records each other node from a removed relationship. It
// uses one blind write per recorded node without reading that node's search
// state. The access work follows these conditions:
//
//   - It skips nil IDs and the deleted node's ID.
//   - It schedules one access work item for the deleted node when it records
//     at least one other node.
//   - That item schedules access work for at most 100 recorded nodes per
//     slice.
func (s *NodeStore) Delete(ctx context.Context, orgID, nodeID uuid.UUID) (err error) {
	defer telemetry.FDBOp(ctx, "store.node.delete")(&err)
	changes, err := beginMutationAudit(ctx, orgID)
	if err != nil {
		return err
	}

	current, err := s.Get(ctx, orgID, nodeID)
	if err != nil || current == nil {
		return err
	}

	transactionErr := runNodeMutation(
		ctx,
		s.db,
		"node.delete",
		func(tr fdb.Transaction) error {
			changes.startAttempt()
			if _, err := s.fenceAttempt(ctx, tr, orgID, nodeID); err != nil {
				return err
			}

			if err := s.clearNode(ctx, tr, current, changes); err != nil {
				return err
			}

			return changes.append(ctx, tr)
		},
	)
	if transactionErr != nil {
		if searchFailureWasLogged(transactionErr) {
			return transactionErr
		}

		err = fmt.Errorf("delete node %s: %w", nodeID, transactionErr)
		telemetry.L(ctx).ErrorContext(
			ctx,
			"node.delete_failed",
			slog.String("err", err.Error()),
			slog.String("node_id", nodeID.String()),
		)

		return err
	}

	changes.finish(ctx)

	return nil
}

// clearNode clears the records, indexes, references, and relationships of
// current inside tr. When search work is enabled, it schedules retirement of
// the node's pages and access work for the recorded nodes at the other ends
// of removed relationships. It reads the stored node record and records its
// removal in changes. clearSourceRelationships and clearTargetRelationships
// read stored relationship values inside tr and record removals for
// relationships with decodable keys.
func (s *NodeStore) clearNode(
	ctx context.Context,
	tr fdb.Transaction,
	current *node.Node,
	changes *mutationAudit,
) error {
	orgID, nodeID := current.OrgID, current.ID
	encoded, err := tr.Get(fdb.Key(nodeInstanceKey(orgID, current.NodeType, nodeID))).Get()
	if err != nil {
		return searchReadFailure(ctx, "read node "+nodeID.String(), err)
	}

	if err := changes.record(nodeTarget(current), encoded, nil); err != nil {
		return nodeOperationFailure(ctx, "record the deletion of node "+nodeID.String(), err)
	}

	var deletion searchWorkRecord
	if s.searchWork {
		deletion, err = scheduleSearchChange(
			ctx,
			tr,
			s.clock.Now(),
			orgID,
			nodeID,
			searchChangeDeletion,
		)
		if err != nil {
			return err
		}
	}

	tr.Clear(fdb.Key(nodeInstanceKey(orgID, current.NodeType, nodeID)))
	tr.Clear(fdb.Key(nodeViewKey(orgID, current.NodeType, nodeID)))
	tr.Clear(fdb.Key(nodeResolveKey(nodeID)))
	sources, err := clearSourceRelationships(ctx, tr, orgID, nodeID, changes)
	if err != nil {
		return err
	}

	targets, err := clearTargetRelationships(ctx, tr, orgID, nodeID, changes)
	if err != nil {
		return err
	}

	if s.searchWork {
		if err := scheduleDeletedFanout(
			ctx,
			tr,
			deletion,
			append(sources, targets...),
		); err != nil {
			return err
		}
	}

	for propName, value := range current.Props {
		tr.Clear(
			fdb.Key(
				nodeByPropertyKey(
					orgID,
					current.NodeType,
					propName,
					encodePropertyValue(value),
					nodeID,
				),
			),
		)
	}

	return clearReferenceKeys(tr, orgID, nodeID)
}

// clearSourceRelationships clears every relationship from nodeID. For each
// key with a valid tuple and target UUID, it clears the reverse key, records
// the stored value as removed in changes, and returns the target. It skips
// those actions for malformed keys before clearing the entire forward range.
func clearSourceRelationships(
	ctx context.Context,
	tr fdb.Transaction,
	orgID uuid.UUID,
	nodeID uuid.UUID,
	changes *mutationAudit,
) ([]uuid.UUID, error) {
	rangeValue, err := fdb.PrefixRange(relationshipPrefixBySource(orgID, nodeID, ""))
	if err != nil {
		return nil, relationshipRangeFailure(ctx, "source", nodeID, err)
	}

	items, err := tr.GetRange(rangeValue, fdb.RangeOptions{}).GetSliceWithError()
	if err != nil {
		return nil, searchReadFailure(
			ctx,
			"read source relationships for node "+nodeID.String(),
			err,
		)
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
			if err := changes.record(
				relationshipTarget(nodeID, relationType, targetID),
				item.Value,
				nil,
			); err != nil {
				return nil, nodeOperationFailure(
					ctx,
					"record a removed relationship of node "+nodeID.String(),
					err,
				)
			}
		}
	}

	tr.ClearRange(rangeValue)

	return targets, nil
}

// clearTargetRelationships clears every reverse relationship key to nodeID.
// For each key with a valid tuple and source UUID, it clears the forward
// key, records its stored value as removed in changes, and returns the
// source. It skips those actions for malformed keys before clearing the
// entire reverse range.
func clearTargetRelationships(
	ctx context.Context,
	tr fdb.Transaction,
	orgID uuid.UUID,
	nodeID uuid.UUID,
	changes *mutationAudit,
) ([]uuid.UUID, error) {
	rangeValue, err := fdb.PrefixRange(relationshipReversePrefixByTarget(orgID, nodeID, ""))
	if err != nil {
		return nil, relationshipRangeFailure(ctx, "target", nodeID, err)
	}

	items, err := tr.GetRange(rangeValue, fdb.RangeOptions{}).GetSliceWithError()
	if err != nil {
		return nil, searchReadFailure(
			ctx,
			"read target relationships for node "+nodeID.String(),
			err,
		)
	}

	incoming := make([]*node.Relationship, 0, len(items))
	for _, item := range items {
		values, unpackErr := tuple.Unpack(stripPrefix(item.Key))
		if unpackErr != nil || len(values) < 5 {
			continue
		}

		relationType, _ := values[3].(string)
		sourceText, _ := values[4].(string)
		sourceID, parseErr := uuid.Parse(sourceText)
		if parseErr == nil {
			incoming = append(
				incoming,
				&node.Relationship{
					OrgID:        orgID,
					SourceID:     sourceID,
					RelationType: relationType,
					TargetID:     nodeID,
					CreatedBy:    uuid.Nil,
					CreatedAt:    time.Time{},
					Props:        nil,
				},
			)
		}
	}

	if err := removeRelationships(ctx, tr, incoming, changes); err != nil {
		return nil, err
	}

	tr.ClearRange(rangeValue)

	sources := make([]uuid.UUID, 0, len(incoming))
	for _, relationship := range incoming {
		sources = append(sources, relationship.SourceID)
	}

	return sources, nil
}

func relationshipRangeFailure(
	ctx context.Context,
	direction string,
	nodeID uuid.UUID,
	err error,
) error {
	wrapped := fmt.Errorf("create %s relationship range for node %s: %w", direction, nodeID, err)
	telemetry.L(ctx).ErrorContext(
		ctx,
		"node.relationship.range_failed",
		slog.String("err", wrapped.Error()),
		slog.String("node_id", nodeID.String()),
	)

	return loggedSearchError{err: wrapped}
}
