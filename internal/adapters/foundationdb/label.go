//go:build fdb

package foundationdb

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
)

type LabelOnNodeValue struct {
	AddedBy uuid.UUID `json:"added_by"`
	AddedAt time.Time `json:"added_at"`
}

// NodeLabelStore implements node.NodeLabelRepository.
type NodeLabelStore struct{ db fdb.Database }

func newNodeLabelStore(db fdb.Database) *NodeLabelStore { return &NodeLabelStore{db: db} }

// SetAll replaces all labels on a node atomically.
func (s *NodeLabelStore) SetAll(_ context.Context, orgID, nodeID uuid.UUID, labelIDs []uuid.UUID, addedBy uuid.UUID) error {
	existing, err := s.ListByNode(context.Background(), orgID, nodeID)
	if err != nil {
		return fmt.Errorf("label set all: %w", err)
	}
	val, _ := json.Marshal(LabelOnNodeValue{AddedBy: addedBy, AddedAt: time.Now().UTC()})
	_, err = s.db.Transact(func(tr fdb.Transaction) (any, error) {
		org := orgID.String()
		node := nodeID.String()
		for _, lid := range existing {
			tr.Clear(fdb.Key(tuple.Tuple{keyLabelOnNode, org, node, lid.String()}.Pack()))
			tr.Clear(fdb.Key(tuple.Tuple{keyIssuesWithLabel, org, lid.String(), node}.Pack()))
		}
		for _, lid := range labelIDs {
			tr.Set(fdb.Key(tuple.Tuple{keyLabelOnNode, org, node, lid.String()}.Pack()), val)
			tr.Set(fdb.Key(tuple.Tuple{keyIssuesWithLabel, org, lid.String(), node}.Pack()), nil)
		}
		return nil, nil
	})
	return err
}

// ListByNode returns all labelIDs on a node.
func (s *NodeLabelStore) ListByNode(_ context.Context, orgID, nodeID uuid.UUID) ([]uuid.UUID, error) {
	pr, _ := fdb.PrefixRange(tuple.Tuple{keyLabelOnNode, orgID.String(), nodeID.String()}.Pack())
	vals, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.GetRange(pr, fdb.RangeOptions{}).GetSliceWithError()
	})
	if err != nil {
		return nil, fmt.Errorf("label list by node: %w", err)
	}
	return extractLastUUIDs(vals.([]fdb.KeyValue), 3), nil
}

// ListByLabel returns all nodeIDs with a given label.
func (s *NodeLabelStore) ListByLabel(_ context.Context, orgID, labelID uuid.UUID) ([]uuid.UUID, error) {
	pr, _ := fdb.PrefixRange(tuple.Tuple{keyIssuesWithLabel, orgID.String(), labelID.String()}.Pack())
	vals, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.GetRange(pr, fdb.RangeOptions{}).GetSliceWithError()
	})
	if err != nil {
		return nil, fmt.Errorf("label list by label: %w", err)
	}
	return extractLastUUIDs(vals.([]fdb.KeyValue), 3), nil
}
