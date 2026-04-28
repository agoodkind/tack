package foundationdb

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// RelationshipStore implements node.RelationshipRepository using FoundationDB.
type RelationshipStore struct {
	db fdb.Database
}

func NewRelationshipStore(db fdb.Database) *RelationshipStore {
	return &RelationshipStore{db: db}
}

func (s *RelationshipStore) Add(ctx context.Context, rel *node.Relationship) (err error) {
	defer telemetry.FDBOp(ctx, "store.relationship.add")(&err)
	metadata, err := json.Marshal(rel)
	if err != nil {
		return fmt.Errorf("marshal relationship: %w", err)
	}
	_, err = s.db.Transact(func(tr fdb.Transaction) (any, error) {
		tr.Set(fdb.Key(relationshipKey(rel.OrgID, rel.SourceID, rel.RelationType, rel.TargetID)), metadata)
		tr.Set(fdb.Key(relationshipReverseKey(rel.OrgID, rel.TargetID, rel.RelationType, rel.SourceID)), []byte{})
		return nil, nil
	})
	return
}

func (s *RelationshipStore) Remove(ctx context.Context, orgID, sourceID uuid.UUID, relationType string, targetID uuid.UUID) (err error) {
	defer telemetry.FDBOp(ctx, "store.relationship.remove")(&err)
	_, err = s.db.Transact(func(tr fdb.Transaction) (any, error) {
		tr.Clear(fdb.Key(relationshipKey(orgID, sourceID, relationType, targetID)))
		tr.Clear(fdb.Key(relationshipReverseKey(orgID, targetID, relationType, sourceID)))
		return nil, nil
	})
	return
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
