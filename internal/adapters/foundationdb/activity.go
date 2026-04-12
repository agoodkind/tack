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

// ActivityStore implements node.ActivityRepository using FoundationDB.
// Keys are ordered by timestamp so range reads return events chronologically.
type ActivityStore struct {
	db fdb.Database
}

func NewActivityStore(db fdb.Database) *ActivityStore {
	return &ActivityStore{db: db}
}

func (s *ActivityStore) Append(_ context.Context, orgID, workspaceID uuid.UUID, event *node.ActivityEvent) error {
	b, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal activity event: %w", err)
	}
	key := tuple.Tuple{
		keyActivityOnNode,
		orgID.String(),
		workspaceID.String(),
		event.NodeID.String(),
		event.CreatedAt.UnixNano(),
		event.EventID.String(),
	}.Pack()
	_, err = s.db.Transact(func(tr fdb.Transaction) (any, error) {
		tr.Set(fdb.Key(key), b)
		return nil, nil
	})
	return err
}

func (s *ActivityStore) List(_ context.Context, orgID, workspaceID, nodeID uuid.UUID) ([]*node.ActivityEvent, error) {
	prefix := tuple.Tuple{keyActivityOnNode, orgID.String(), workspaceID.String(), nodeID.String()}.Pack()
	pr, err := fdb.PrefixRange(prefix)
	if err != nil {
		return nil, err
	}
	vals, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.GetRange(pr, fdb.RangeOptions{}).GetSliceWithError()
	})
	if err != nil {
		return nil, fmt.Errorf("fdb list activity: %w", err)
	}
	kvs := vals.([]fdb.KeyValue)
	events := make([]*node.ActivityEvent, 0, len(kvs))
	for _, kv := range kvs {
		var ev node.ActivityEvent
		if err := json.Unmarshal(kv.Value, &ev); err != nil {
			return nil, fmt.Errorf("unmarshal activity event: %w", err)
		}
		events = append(events, &ev)
	}
	return events, nil
}
