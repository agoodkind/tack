package foundationdb

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// NodeTypeStore implements node.TypeRepository using FoundationDB.
type NodeTypeStore struct {
	db fdb.Database
}

func NewNodeTypeStore(db fdb.Database) *NodeTypeStore {
	return &NodeTypeStore{db: db}
}

func (s *NodeTypeStore) Set(ctx context.Context, nt *node.NodeType) (err error) {
	defer telemetry.FDBOp(ctx, "store.node_type.set")(&err)
	b, err := json.Marshal(nt)
	if err != nil {
		return fmt.Errorf("marshal node type: %w", err)
	}
	_, err = s.db.Transact(func(tr fdb.Transaction) (any, error) {
		tr.Set(fdb.Key(nodeTypeDefKey(nt.OrgID, nt.ID)), b)
		return nil, nil
	})
	return
}

func (s *NodeTypeStore) Get(ctx context.Context, orgID, typeID uuid.UUID) (out *node.NodeType, err error) {
	defer telemetry.FDBOp(ctx, "store.node_type.get")(&err)
	val, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.Get(fdb.Key(nodeTypeDefKey(orgID, typeID))).Get()
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

func (s *NodeTypeStore) List(ctx context.Context, orgID uuid.UUID) (types []*node.NodeType, err error) {
	defer telemetry.FDBOp(ctx, "store.node_type.list")(&err)
	pr, err := fdb.PrefixRange(nodeTypeDefPrefix(orgID))
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
	types = make([]*node.NodeType, 0, len(kvs))
	for _, kv := range kvs {
		var nt node.NodeType
		if err := json.Unmarshal(kv.Value, &nt); err != nil {
			return nil, fmt.Errorf("unmarshal node type: %w", err)
		}
		types = append(types, &nt)
	}
	return types, nil
}

func (s *NodeTypeStore) Delete(ctx context.Context, orgID, typeID uuid.UUID) (err error) {
	defer telemetry.FDBOp(ctx, "store.node_type.delete")(&err)
	_, err = s.db.Transact(func(tr fdb.Transaction) (any, error) {
		tr.Clear(fdb.Key(nodeTypeDefKey(orgID, typeID)))
		return nil, nil
	})
	return
}
