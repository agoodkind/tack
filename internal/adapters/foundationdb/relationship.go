package foundationdb

import (
	"context"
	"encoding/json"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// RelationshipStore implements node.RelationshipRepository using FoundationDB.
// When searchWork is true, each relationship write schedules search access
// work and stamps it with a time from the clock. When writeFence is set,
// Add and Remove run the fence for the source node.
type RelationshipStore struct {
	db         fdb.Database
	clock      clock.Clock
	searchWork bool
	writeFence mutationFence
}

// NewRelationshipStore creates the relationship store. It schedules no
// search work until [Stores.EnableSearchWork] runs.
func NewRelationshipStore(db fdb.Database, source clock.Clock) *RelationshipStore {
	return &RelationshipStore{db: db, clock: source, searchWork: false, writeFence: nil}
}

// Add writes one edge. The same transaction writes the change event of the
// edge with the stored value that the edge replaces.
func (s *RelationshipStore) Add(ctx context.Context, rel *node.Relationship) (err error) {
	defer telemetry.FDBOp(ctx, "store.relationship.add")(&err)
	changes, err := beginMutationAudit(ctx, rel.OrgID)
	if err != nil {
		return err
	}
	searchScheduleFailed := false
	err = runNodeMutation(ctx, s.db, "add relationship "+rel.RelationType, func(tr fdb.Transaction) error {
		searchScheduleFailed = false
		changes.startAttempt()
		if _, err := s.fenceAttempt(ctx, tr, rel.OrgID, rel.SourceID); err != nil {
			return err
		}
		if err := addRelationships(ctx, tr, []*node.Relationship{rel}, changes); err != nil {
			return err
		}
		if s.searchWork {
			if err := scheduleRelatedSearchWork(ctx, tr, s.clock.Now(), []node.RelationshipChanges{
				{Add: []*node.Relationship{rel}, Remove: []*node.Relationship{}},
			}); err != nil {
				searchScheduleFailed = true
				return err
			}
		}
		return changes.append(ctx, tr)
	})
	if err != nil {
		if searchScheduleFailed {
			logSearchScheduleFailure(ctx, err, rel.SourceID, rel.TargetID)
		} else {
			logRelationshipFailure(ctx, "relationship.add_failed", err, rel.SourceID, rel.TargetID)
		}
		return err
	}
	changes.finish(ctx)
	return nil
}

// Remove clears one edge. It refuses to clear the source node's only
// hierarchy parent edge and returns an error that wraps domain.ErrFailedPrecondition.
// The same transaction writes the change event of the removed edge.
func (s *RelationshipStore) Remove(ctx context.Context, orgID, sourceID uuid.UUID, relationType string, targetID uuid.UUID) (err error) {
	defer telemetry.FDBOp(ctx, "store.relationship.remove")(&err)
	changes, err := beginMutationAudit(ctx, orgID)
	if err != nil {
		return err
	}
	searchScheduleFailed := false
	guardFailed := false
	err = runNodeMutation(ctx, s.db, "remove relationship "+relationType, func(tr fdb.Transaction) error {
		searchScheduleFailed = false
		guardFailed = false
		changes.startAttempt()
		if err := refuseOnlyParentRemoval(ctx, tr, orgID, sourceID, relationType, targetID); err != nil {
			guardFailed = true
			return err
		}
		if _, err := s.fenceAttempt(ctx, tr, orgID, sourceID); err != nil {
			return err
		}
		changed := &node.Relationship{
			OrgID: orgID, SourceID: sourceID, RelationType: relationType, TargetID: targetID,
			CreatedBy: uuid.Nil, CreatedAt: time.Time{}, Props: map[string]json.RawMessage{},
		}
		if err := removeRelationships(ctx, tr, []*node.Relationship{changed}, changes); err != nil {
			return err
		}
		if s.searchWork {
			if err := scheduleRelatedSearchWork(ctx, tr, s.clock.Now(), []node.RelationshipChanges{
				{Add: []*node.Relationship{}, Remove: []*node.Relationship{changed}},
			}); err != nil {
				searchScheduleFailed = true
				return err
			}
		}
		return changes.append(ctx, tr)
	})
	if err != nil {
		if searchScheduleFailed {
			logSearchScheduleFailure(ctx, err, sourceID, targetID)
		} else if !guardFailed || !searchFailureWasLogged(err) {
			logRelationshipFailure(ctx, "relationship.remove_failed", err, sourceID, targetID)
		}
		return err
	}
	changes.finish(ctx)
	return nil
}

func (s *RelationshipStore) ListBySource(ctx context.Context, orgID, sourceID uuid.UUID, relationType string) (rels []*node.Relationship, err error) {
	defer telemetry.FDBOp(ctx, "store.relationship.list_by_source")(&err)
	pr, err := fdb.PrefixRange(relationshipPrefixBySource(orgID, sourceID, relationType))
	if err != nil {
		return nil, nodeOperationFailure(ctx, "create the relationship range of node "+sourceID.String(), err)
	}
	var kvs []fdb.KeyValue
	err = runNodeReadTransaction(ctx, s.db, "list relationships by source", func(tr fdb.Transaction) error {
		var readErr error
		kvs, readErr = tr.GetRange(pr, fdb.RangeOptions{}).GetSliceWithError()
		if readErr != nil {
			return searchReadFailure(ctx, "read relationships of node "+sourceID.String(), readErr)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return decodeRelationships(kvs), nil
}

// ListByTarget reads the reverse keys of targetID, which store empty values,
// and then reads the forward value of each relationship.
func (s *RelationshipStore) ListByTarget(ctx context.Context, orgID, targetID uuid.UUID, relationType string) (rels []*node.Relationship, err error) {
	defer telemetry.FDBOp(ctx, "store.relationship.list_by_target")(&err)
	revPR, err := fdb.PrefixRange(relationshipReversePrefixByTarget(orgID, targetID, relationType))
	if err != nil {
		return nil, nodeOperationFailure(ctx, "create the reverse relationship range of node "+targetID.String(), err)
	}
	err = runNodeReadTransaction(ctx, s.db, "list relationships by target", func(tr fdb.Transaction) error {
		var readErr error
		rels, readErr = readRelationshipsByTarget(ctx, tr, revPR, orgID, targetID)
		return readErr
	})
	if err != nil {
		return nil, err
	}
	return rels, nil
}

// readRelationshipsByTarget reads the forward value of each reverse key in
// revPR inside tr. It skips a key that it cannot decode and a forward value
// that is missing or cannot be decoded.
func readRelationshipsByTarget(ctx context.Context, tr fdb.Transaction, revPR fdb.KeyRange, orgID, targetID uuid.UUID) ([]*node.Relationship, error) {
	revKVs, err := tr.GetRange(revPR, fdb.RangeOptions{}).GetSliceWithError()
	if err != nil {
		return nil, searchReadFailure(ctx, "read relationships to node "+targetID.String(), err)
	}
	rs := make([]*node.Relationship, 0, len(revKVs))
	for _, kv := range revKVs {
		t, terr := tuple.Unpack(stripPrefix(kv.Key))
		if terr != nil || len(t) < 5 {
			continue
		}
		relType, _ := t[3].(string)
		sourceStr, _ := t[4].(string)
		sourceID, perr := uuid.Parse(sourceStr)
		if perr != nil {
			continue
		}
		fwd, err := tr.Get(fdb.Key(relationshipKey(orgID, sourceID, relType, targetID))).Get()
		if err != nil || len(fwd) == 0 {
			continue
		}
		var rel node.Relationship
		if err := json.Unmarshal(fwd, &rel); err != nil {
			continue
		}
		rs = append(rs, &rel)
	}
	return rs, nil
}

func decodeRelationships(kvs []fdb.KeyValue) []*node.Relationship {
	rels := make([]*node.Relationship, 0, len(kvs))
	for _, kv := range kvs {
		if len(kv.Value) == 0 {
			continue
		}
		var rel node.Relationship
		if err := json.Unmarshal(kv.Value, &rel); err != nil {
			continue
		}
		rels = append(rels, &rel)
	}
	return rels
}
