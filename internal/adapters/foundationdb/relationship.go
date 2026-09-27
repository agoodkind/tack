package foundationdb

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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
// work stamped by the clock.
type RelationshipStore struct {
	db         fdb.Database
	clock      clock.Clock
	searchWork bool
}

// NewRelationshipStore creates the relationship store. It schedules no
// search work until [Stores.EnableSearchWork] runs.
func NewRelationshipStore(db fdb.Database, source clock.Clock) *RelationshipStore {
	return &RelationshipStore{db: db, clock: source, searchWork: false}
}

func (s *RelationshipStore) Add(ctx context.Context, rel *node.Relationship) (err error) {
	defer telemetry.FDBOp(ctx, "store.relationship.add")(&err)
	metadata, err := json.Marshal(rel)
	if err != nil {
		return fmt.Errorf("marshal relationship: %w", err)
	}
	searchScheduleFailed := false
	_, err = s.db.Transact(func(tr fdb.Transaction) (any, error) {
		searchScheduleFailed = false
		tr.Set(fdb.Key(relationshipKey(rel.OrgID, rel.SourceID, rel.RelationType, rel.TargetID)), metadata)
		tr.Set(fdb.Key(relationshipReverseKey(rel.OrgID, rel.TargetID, rel.RelationType, rel.SourceID)), []byte{})
		if s.searchWork {
			if err := scheduleRelatedSearchWork(ctx, tr, s.clock.Now(), []node.RelationshipChanges{
				{Add: []*node.Relationship{rel}, Remove: []*node.Relationship{}},
			}); err != nil {
				searchScheduleFailed = true
				return nil, err
			}
		}
		return nil, writeStagedIntent(ctx, tr)
	})
	if err != nil {
		wrapped := fmt.Errorf("add relationship %s: %w", rel.RelationType, err)
		logRelationshipFailure(ctx, "relationship.add_failed", searchScheduleFailed, wrapped, rel.SourceID, rel.TargetID)
		return wrapped
	}
	commitStagedIntent(ctx)
	return nil
}

func (s *RelationshipStore) Remove(ctx context.Context, orgID, sourceID uuid.UUID, relationType string, targetID uuid.UUID) (err error) {
	defer telemetry.FDBOp(ctx, "store.relationship.remove")(&err)
	searchScheduleFailed := false
	_, err = s.db.Transact(func(tr fdb.Transaction) (any, error) {
		searchScheduleFailed = false
		tr.Clear(fdb.Key(relationshipKey(orgID, sourceID, relationType, targetID)))
		tr.Clear(fdb.Key(relationshipReverseKey(orgID, targetID, relationType, sourceID)))
		changed := &node.Relationship{
			OrgID: orgID, SourceID: sourceID, RelationType: relationType, TargetID: targetID,
			CreatedBy: uuid.Nil, CreatedAt: time.Time{}, Props: map[string]json.RawMessage{},
		}
		if s.searchWork {
			if err := scheduleRelatedSearchWork(ctx, tr, s.clock.Now(), []node.RelationshipChanges{
				{Add: []*node.Relationship{}, Remove: []*node.Relationship{changed}},
			}); err != nil {
				searchScheduleFailed = true
				return nil, err
			}
		}
		return nil, writeStagedIntent(ctx, tr)
	})
	if err != nil {
		wrapped := fmt.Errorf("remove relationship %s: %w", relationType, err)
		logRelationshipFailure(ctx, "relationship.remove_failed", searchScheduleFailed, wrapped, sourceID, targetID)
		return wrapped
	}
	commitStagedIntent(ctx)
	return nil
}

// logRelationshipFailure logs one failed relationship write. A search
// scheduling failure logs search.work.schedule_failed unless the search code
// already logged it. Every other failure logs failedEvent.
func logRelationshipFailure(ctx context.Context, failedEvent string, searchScheduleFailed bool, err error, sourceID, targetID uuid.UUID) {
	event := failedEvent
	if searchScheduleFailed {
		if searchFailureWasLogged(err) {
			return
		}
		event = "search.work.schedule_failed"
	}
	telemetry.L(ctx).ErrorContext(ctx, event, slog.String("err", err.Error()), slog.String("source_id", sourceID.String()), slog.String("target_id", targetID.String()))
}

func (s *RelationshipStore) ListBySource(ctx context.Context, orgID, sourceID uuid.UUID, relationType string) (rels []*node.Relationship, err error) {
	defer telemetry.FDBOp(ctx, "store.relationship.list_by_source")(&err)
	pr, err := fdb.PrefixRange(relationshipPrefixBySource(orgID, sourceID, relationType))
	if err != nil {
		return nil, err
	}
	vals, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.GetRange(pr, fdb.RangeOptions{}).GetSliceWithError()
	})
	if err != nil {
		return nil, fmt.Errorf("fdb list rels by source: %w", err)
	}
	return decodeRelationships(vals.([]fdb.KeyValue)), nil
}

func (s *RelationshipStore) ListByTarget(ctx context.Context, orgID, targetID uuid.UUID, relationType string) (rels []*node.Relationship, err error) {
	defer telemetry.FDBOp(ctx, "store.relationship.list_by_target")(&err)
	// The reverse prefix only stores nil values; for metadata we need the
	// forward entries. Scan the reverse prefix, then batch-fetch the forward
	// metadata values.
	revPrefix := relationshipReversePrefixByTarget(orgID, targetID, relationType)
	revPR, err := fdb.PrefixRange(revPrefix)
	if err != nil {
		return nil, err
	}
	vals, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		revKVs, err := tr.GetRange(revPR, fdb.RangeOptions{}).GetSliceWithError()
		if err != nil {
			return nil, err
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
	})
	if err != nil {
		return nil, fmt.Errorf("fdb list rels by target: %w", err)
	}
	return vals.([]*node.Relationship), nil
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
