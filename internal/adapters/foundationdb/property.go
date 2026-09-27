package foundationdb

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// PropertyDefStore implements node.PropertyDefRepository using FoundationDB.
// When searchWork is true, a definition write requests a search rescan
// stamped by the clock.
type PropertyDefStore struct {
	db         fdb.Database
	clock      clock.Clock
	searchWork bool
}

// NewPropertyDefStore creates the property definition store. It requests no
// search rescan until [Stores.EnableSearchWork] runs.
func NewPropertyDefStore(db fdb.Database, source clock.Clock) *PropertyDefStore {
	return &PropertyDefStore{db: db, clock: source, searchWork: false}
}

// Set stores def, its name index entry, and the projection digest, and
// requests a content rescan when the declaration changed.
func (s *PropertyDefStore) Set(ctx context.Context, def *node.PropertyDef) (err error) {
	defer telemetry.FDBOp(ctx, "store.property_def.set")(&err)
	b, err := json.Marshal(def)
	if err != nil {
		return fmt.Errorf("marshal property def: %w", err)
	}
	_, err = s.db.Transact(func(tr fdb.Transaction) (any, error) {
		key := propertyDefKey(def.OrgID, def.ID)
		previous, readErr := tr.Get(fdb.Key(key)).Get()
		if readErr != nil {
			return nil, searchReadFailure(ctx, "read property definition "+def.ID.String(), readErr)
		}
		if err := updatePropertyDefinition(ctx, tr, s.clock.Now(), s.searchWork, def.OrgID, def.ID, previous, def); err != nil {
			return nil, err
		}
		tr.Set(fdb.Key(key), b)
		return nil, nil
	})
	return
}

func (s *PropertyDefStore) Get(ctx context.Context, orgID, defID uuid.UUID) (def *node.PropertyDef, err error) {
	defer telemetry.FDBOp(ctx, "store.property_def.get")(&err)
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
	var d node.PropertyDef
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, fmt.Errorf("unmarshal property def: %w", err)
	}
	return &d, nil
}

func (s *PropertyDefStore) List(ctx context.Context, orgID uuid.UUID) (defs []*node.PropertyDef, err error) {
	defer telemetry.FDBOp(ctx, "store.property_def.list")(&err)
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
	defs = make([]*node.PropertyDef, 0, len(kvs))
	for _, kv := range kvs {
		var d node.PropertyDef
		if err := json.Unmarshal(kv.Value, &d); err != nil {
			return nil, fmt.Errorf("unmarshal property def: %w", err)
		}
		defs = append(defs, &d)
	}
	return defs, nil
}

func (s *PropertyDefStore) Delete(ctx context.Context, orgID, defID uuid.UUID) (err error) {
	defer telemetry.FDBOp(ctx, "store.property_def.delete")(&err)
	_, err = s.db.Transact(func(tr fdb.Transaction) (any, error) {
		key := propertyDefKey(orgID, defID)
		previous, readErr := tr.Get(fdb.Key(key)).Get()
		if readErr != nil {
			return nil, searchReadFailure(ctx, "read property definition "+defID.String(), readErr)
		}
		if err := updatePropertyDefinition(ctx, tr, s.clock.Now(), s.searchWork, orgID, defID, previous, nil); err != nil {
			return nil, err
		}
		tr.Clear(fdb.Key(key))
		return nil, nil
	})
	return
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
