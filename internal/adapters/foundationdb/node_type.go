//go:build fdb

package foundationdb

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/agoodkind/tack/internal/domain/node"
	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
)

// NodeTypeStore implements node.TypeRepository using FoundationDB.
type NodeTypeStore struct {
	db fdb.Database
}

func NewNodeTypeStore(db fdb.Database) *NodeTypeStore {
	return &NodeTypeStore{db: db}
}

func (s *NodeTypeStore) Set(_ context.Context, nt *node.NodeType) error {
	b, err := json.Marshal(nt)
	if err != nil {
		return fmt.Errorf("marshal node type: %w", err)
	}
	key := tuple.Tuple{prefixNodeTypes, nt.OrgID.String(), nt.ID.String()}.Pack()
	_, err = s.db.Transact(func(tr fdb.Transaction) (any, error) {
		tr.Set(fdb.Key(key), b)
		return nil, nil
	})
	return err
}

func (s *NodeTypeStore) Get(_ context.Context, orgID, typeID uuid.UUID) (*node.NodeType, error) {
	key := tuple.Tuple{prefixNodeTypes, orgID.String(), typeID.String()}.Pack()
	val, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.Get(fdb.Key(key)).Get()
	})
	if err != nil {
		return nil, fmt.Errorf("fdb get node type: %w", err)
	}
	b, ok := val.([]byte)
	if !ok || len(b) == 0 {
		return nil, nil
	}
	var nt node.NodeType
	if err := json.Unmarshal(b, &nt); err != nil {
		return nil, fmt.Errorf("unmarshal node type: %w", err)
	}
	return &nt, nil
}

func (s *NodeTypeStore) List(_ context.Context, orgID uuid.UUID) ([]*node.NodeType, error) {
	prefix := tuple.Tuple{prefixNodeTypes, orgID.String()}.Pack()
	pr, err := fdb.PrefixRange(prefix)
	if err != nil {
		return nil, err
	}
	vals, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.GetRange(pr, fdb.RangeOptions{}).GetSliceWithError()
	})
	if err != nil {
		return nil, fmt.Errorf("fdb list node types: %w", err)
	}
	kvs := vals.([]fdb.KeyValue)
	types := make([]*node.NodeType, 0, len(kvs))
	for _, kv := range kvs {
		var nt node.NodeType
		if err := json.Unmarshal(kv.Value, &nt); err != nil {
			return nil, fmt.Errorf("unmarshal node type: %w", err)
		}
		types = append(types, &nt)
	}
	return types, nil
}

func (s *NodeTypeStore) Delete(_ context.Context, orgID, typeID uuid.UUID) error {
	key := tuple.Tuple{prefixNodeTypes, orgID.String(), typeID.String()}.Pack()
	_, err := s.db.Transact(func(tr fdb.Transaction) (any, error) {
		tr.Clear(fdb.Key(key))
		return nil, nil
	})
	return err
}
