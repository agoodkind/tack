package foundationdb

import (
	"context"
	"encoding/json"
	"fmt"

	"goodkind.io/tack/internal/domain/node"
	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
)

// PropertyStore implements node.PropertyRepository using FoundationDB.
type PropertyStore struct {
	db fdb.Database
}

func NewPropertyStore(db fdb.Database) *PropertyStore {
	return &PropertyStore{db: db}
}

func (s *PropertyStore) SetDef(_ context.Context, def *node.PropertyDef) error {
	b, err := json.Marshal(def)
	if err != nil {
		return fmt.Errorf("marshal property def: %w", err)
	}
	_, err = s.db.Transact(func(tr fdb.Transaction) (any, error) {
		tr.Set(fdb.Key(propertyDefKey(def)), b)
		return nil, nil
	})
	return err
}

func (s *PropertyStore) GetDef(_ context.Context, orgID, defID uuid.UUID) (*node.PropertyDef, error) {
	key := tuple.Tuple{keyPropertyDefinition, orgID.String(), defID.String()}.Pack()
	val, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.Get(fdb.Key(key)).Get()
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

func (s *PropertyStore) ListDefs(_ context.Context, orgID, workspaceID uuid.UUID, projectID *uuid.UUID) ([]*node.PropertyDef, error) {
	prefix := tuple.Tuple{keyPropertyDefinition, orgID.String()}.Pack()
	pr, err := fdb.PrefixRange(prefix)
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
		if def.WorkspaceID != nil && *def.WorkspaceID != workspaceID {
			continue
		}
		if def.ProjectID != nil && (projectID == nil || *def.ProjectID != *projectID) {
			continue
		}
		defs = append(defs, &def)
	}
	return defs, nil
}

func (s *PropertyStore) DeleteDef(_ context.Context, def *node.PropertyDef) error {
	_, err := s.db.Transact(func(tr fdb.Transaction) (any, error) {
		tr.Clear(fdb.Key(propertyDefKey(def)))
		return nil, nil
	})
	return err
}

func (s *PropertyStore) SetValue(_ context.Context, orgID, nodeID, propDefID uuid.UUID, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal property value: %w", err)
	}
	key := tuple.Tuple{keyPropertyValueOnNode, orgID.String(), nodeID.String(), propDefID.String()}.Pack()
	_, err = s.db.Transact(func(tr fdb.Transaction) (any, error) {
		tr.Set(fdb.Key(key), b)
		return nil, nil
	})
	return err
}

func (s *PropertyStore) GetValues(_ context.Context, orgID, nodeID uuid.UUID) (node.Properties, error) {
	prefix := tuple.Tuple{keyPropertyValueOnNode, orgID.String(), nodeID.String()}.Pack()
	pr, err := fdb.PrefixRange(prefix)
	if err != nil {
		return nil, err
	}
	vals, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.GetRange(pr, fdb.RangeOptions{}).GetSliceWithError()
	})
	if err != nil {
		return nil, fmt.Errorf("fdb get property values: %w", err)
	}
	kvs := vals.([]fdb.KeyValue)
	result := make(node.Properties, len(kvs))
	for _, kv := range kvs {
		t, err := tuple.Unpack(kv.Key)
		if err != nil || len(t) < 4 {
			continue
		}
		idStr, ok := t[3].(string)
		if !ok {
			continue
		}
		id, err := uuid.Parse(idStr)
		if err != nil {
			continue
		}
		result[id] = json.RawMessage(kv.Value)
	}
	return result, nil
}

func (s *PropertyStore) DeleteValue(_ context.Context, orgID, nodeID, propDefID uuid.UUID) error {
	key := tuple.Tuple{keyPropertyValueOnNode, orgID.String(), nodeID.String(), propDefID.String()}.Pack()
	_, err := s.db.Transact(func(tr fdb.Transaction) (any, error) {
		tr.Clear(fdb.Key(key))
		return nil, nil
	})
	return err
}

func propertyDefKey(def *node.PropertyDef) []byte {
	if def.ProjectID != nil {
		return tuple.Tuple{
			keyPropertyDefinition,
			def.OrgID.String(),
			def.WorkspaceID.String(),
			def.ProjectID.String(),
			def.ID.String(),
		}.Pack()
	}
	if def.WorkspaceID != nil {
		return tuple.Tuple{
			keyPropertyDefinition,
			def.OrgID.String(),
			def.WorkspaceID.String(),
			def.ID.String(),
		}.Pack()
	}
	return tuple.Tuple{
		keyPropertyDefinition,
		def.OrgID.String(),
		def.ID.String(),
	}.Pack()
}
