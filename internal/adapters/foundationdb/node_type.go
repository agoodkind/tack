package foundationdb

import (
	"context"
	"encoding/json"

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

// Set stores nt and its type-key index entry. It increments the
// organization's metadata epoch when the stored record changes. When search
// work is enabled and the type key, features, or hierarchy declaration
// changed, Set also requests an access rescan. The same transaction writes
// the change event of the record.
func (s *NodeTypeStore) Set(ctx context.Context, nt *node.NodeType) (err error) {
	defer telemetry.FDBOp(ctx, "store.node_type.set")(&err)
	b, err := json.Marshal(nt)
	if err != nil {
		return nodeOperationFailure(ctx, "marshal node type "+nt.ID.String(), err)
	}
	changes, err := beginMutationAudit(ctx, nt.OrgID)
	if err != nil {
		return err
	}
	err = runNodeMutation(ctx, s.db, "set node type "+nt.ID.String(), func(tr fdb.Transaction) error {
		changes.startAttempt()
		key := nodeTypeDefKey(nt.OrgID, nt.ID)
		previous, readErr := tr.Get(fdb.Key(key)).Get()
		if readErr != nil {
			return searchReadFailure(ctx, "read node type "+nt.ID.String(), readErr)
		}
		if err := updateNodeTypeIndex(ctx, tr, s.clock.Now(), s.searchWork, nt.OrgID, nt.ID, previous, nt); err != nil {
			return err
		}
		tr.Set(fdb.Key(key), b)
		return recordMetadataChange(ctx, tr, changes, nodeTypeTarget(nt.ID, nt.TypeKey), previous, b)
	})
	if err != nil {
		return err
	}
	changes.finish(ctx)
	return nil
}

func (s *NodeTypeStore) Get(ctx context.Context, orgID, typeID uuid.UUID) (out *node.NodeType, err error) {
	defer telemetry.FDBOp(ctx, "store.node_type.get")(&err)
	var encoded []byte
	err = runNodeReadTransaction(ctx, s.db, "get node type "+typeID.String(), func(tr fdb.Transaction) error {
		var readErr error
		encoded, readErr = tr.Get(fdb.Key(nodeTypeDefKey(orgID, typeID))).Get()
		if readErr != nil {
			return searchReadFailure(ctx, "read node type "+typeID.String(), readErr)
		}
		return nil
	})
	if err != nil || len(encoded) == 0 {
		return nil, err
	}
	var nt node.NodeType
	if err := json.Unmarshal(encoded, &nt); err != nil {
		return nil, nodeOperationFailure(ctx, "unmarshal node type "+typeID.String(), err)
	}
	return &nt, nil
}

func (s *NodeTypeStore) List(ctx context.Context, orgID uuid.UUID) (types []*node.NodeType, err error) {
	defer telemetry.FDBOp(ctx, "store.node_type.list")(&err)
	pr, err := fdb.PrefixRange(nodeTypeDefPrefix(orgID))
	if err != nil {
		return nil, nodeOperationFailure(ctx, "create the node type range", err)
	}
	var kvs []fdb.KeyValue
	err = runNodeReadTransaction(ctx, s.db, "list node types", func(tr fdb.Transaction) error {
		var readErr error
		kvs, readErr = tr.GetRange(pr, fdb.RangeOptions{}).GetSliceWithError()
		if readErr != nil {
			return searchReadFailure(ctx, "read node types", readErr)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	types = make([]*node.NodeType, 0, len(kvs))
	for _, kv := range kvs {
		var nt node.NodeType
		if err := json.Unmarshal(kv.Value, &nt); err != nil {
			return nil, nodeOperationFailure(ctx, "unmarshal node type", err)
		}
		types = append(types, &nt)
	}
	return types, nil
}

// Delete removes one node type record and its type-key index entry. The same
// transaction writes the change event of the removed record.
func (s *NodeTypeStore) Delete(ctx context.Context, orgID, typeID uuid.UUID) (err error) {
	defer telemetry.FDBOp(ctx, "store.node_type.delete")(&err)
	changes, err := beginMutationAudit(ctx, orgID)
	if err != nil {
		return err
	}
	err = runNodeMutation(ctx, s.db, "delete node type "+typeID.String(), func(tr fdb.Transaction) error {
		changes.startAttempt()
		key := nodeTypeDefKey(orgID, typeID)
		previous, readErr := tr.Get(fdb.Key(key)).Get()
		if readErr != nil {
			return searchReadFailure(ctx, "read node type "+typeID.String(), readErr)
		}
		if err := updateNodeTypeIndex(ctx, tr, s.clock.Now(), s.searchWork, orgID, typeID, previous, nil); err != nil {
			return err
		}
		tr.Clear(fdb.Key(key))
		target, err := storedNodeTypeTarget(ctx, typeID, previous)
		if err != nil {
			return err
		}
		return recordMetadataChange(ctx, tr, changes, target, previous, nil)
	})
	if err != nil {
		return err
	}
	changes.finish(ctx)
	return nil
}
