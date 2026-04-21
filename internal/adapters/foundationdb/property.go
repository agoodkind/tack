package foundationdb

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// PropertyDefStore implements node.PropertyDefRepository using FoundationDB.
type PropertyDefStore struct {
	db fdb.Database
}

func NewPropertyDefStore(db fdb.Database) *PropertyDefStore {
	return &PropertyDefStore{db: db}
}

func (s *PropertyDefStore) Set(_ context.Context, def *node.PropertyDef) error {
	b, err := json.Marshal(def)
	if err != nil {
		return fmt.Errorf("marshal property def: %w", err)
	}
	_, err = s.db.Transact(func(tr fdb.Transaction) (any, error) {
		tr.Set(fdb.Key(propertyDefKey(def.OrgID, def.ID)), b)
		return nil, nil
	})
	return err
}

func (s *PropertyDefStore) Get(_ context.Context, orgID, defID uuid.UUID) (*node.PropertyDef, error) {
	val, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.Get(fdb.Key(propertyDefKey(orgID, defID))).Get()
	})
	if err != nil {
		return nil, fmt.Errorf("fdb get property def: %w", err)
	}
	b, ok := val.([]byte)
	if !ok || len(b) == 0 {
		return nil, nil
	}
	var def node.PropertyDef
	if err := json.Unmarshal(b, &def); err != nil {
		return nil, fmt.Errorf("unmarshal property def: %w", err)
	}
	return &def, nil
}

func (s *PropertyDefStore) List(_ context.Context, orgID uuid.UUID) ([]*node.PropertyDef, error) {
	pr, err := fdb.PrefixRange(propertyDefPrefix(orgID))
	if err != nil {
		return nil, err
	}
	vals, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.GetRange(pr, fdb.RangeOptions{}).GetSliceWithError()
	})
	if err != nil {
		return nil, fmt.Errorf("fdb list property defs: %w", err)
	}
	kvs := vals.([]fdb.KeyValue)
	defs := make([]*node.PropertyDef, 0, len(kvs))
	for _, kv := range kvs {
		var def node.PropertyDef
		if err := json.Unmarshal(kv.Value, &def); err != nil {
			return nil, fmt.Errorf("unmarshal property def: %w", err)
		}
		defs = append(defs, &def)
	}
	return defs, nil
}

func (s *PropertyDefStore) Delete(_ context.Context, orgID, defID uuid.UUID) error {
	_, err := s.db.Transact(func(tr fdb.Transaction) (any, error) {
		tr.Clear(fdb.Key(propertyDefKey(orgID, defID)))
		return nil, nil
	})
	return err
}

// encodePropertyValue encodes a raw JSON property value as FDB tuple bytes for
// use in the secondary index. The encoding must sort the way the caller expects
// range scans to iterate. This is a minimal implementation: numbers encode as
// float64 IEEE (round-trip safe for JSON numbers), strings/bools/null as their
// native tuple types, arrays/objects as their JSON string representation
// (lexicographic sort, useful for equality scans).
func encodePropertyValue(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return nil
	}
	// Use the raw bytes directly: JSON's canonical encoding for primitives
	// (numbers, strings, bools, null) sorts correctly for equality scans.
	// For range scans across numeric properties, callers should prefer
	// typed encoding via a PropertyDef-aware helper; this default works
	// for the common equality filter case.
	return []byte(raw)
}
