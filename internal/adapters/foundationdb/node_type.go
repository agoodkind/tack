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

// NodeTypeStore implements node.TypeRepository using FoundationDB. When
// searchWork is true, a node type write requests a search rescan stamped by
// the clock.
type NodeTypeStore struct {
	db         fdb.Database
	clock      clock.Clock
	searchWork bool
}

// NewNodeTypeStore creates the node type store. It requests no search
// rescan until [Stores.EnableSearchWork] runs.
func NewNodeTypeStore(db fdb.Database, source clock.Clock) *NodeTypeStore {
	return &NodeTypeStore{db: db, clock: source, searchWork: false}
}

// Set stores nt and its type-key index entry, and requests an access rescan
// when the type key, features, or hierarchy declaration changed.
func (s *NodeTypeStore) Set(ctx context.Context, nt *node.NodeType) (err error) {
	defer telemetry.FDBOp(ctx, "store.node_type.set")(&err)
	b, err := json.Marshal(nt)
	if err != nil {
		return fmt.Errorf("marshal node type: %w", err)
	}
	_, err = s.db.Transact(func(tr fdb.Transaction) (any, error) {
		key := nodeTypeDefKey(nt.OrgID, nt.ID)
		previous, readErr := tr.Get(fdb.Key(key)).Get()
		if readErr != nil {
			return nil, searchReadFailure(ctx, "read node type "+nt.ID.String(), readErr)
		}
		if err := updateNodeTypeIndex(ctx, tr, s.clock.Now(), s.searchWork, nt.OrgID, nt.ID, previous, nt); err != nil {
			return nil, err
		}
		tr.Set(fdb.Key(key), b)
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
		key := nodeTypeDefKey(orgID, typeID)
		previous, readErr := tr.Get(fdb.Key(key)).Get()
		if readErr != nil {
			return nil, searchReadFailure(ctx, "read node type "+typeID.String(), readErr)
		}
		if err := updateNodeTypeIndex(ctx, tr, s.clock.Now(), s.searchWork, orgID, typeID, previous, nil); err != nil {
			return nil, err
		}
		tr.Clear(fdb.Key(key))
		return nil, nil
	})
	return
}
