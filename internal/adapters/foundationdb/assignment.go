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

type AssignmentValue struct {
	AssignedBy uuid.UUID `json:"assigned_by"`
	AssignedAt time.Time `json:"assigned_at"`
}

// AssignmentStore implements node.AssignmentRepository.
type AssignmentStore struct{ db fdb.Database }

func newAssignmentStore(db fdb.Database) *AssignmentStore { return &AssignmentStore{db: db} }

// SetAll replaces all assignees on a node atomically.
func (s *AssignmentStore) SetAll(_ context.Context, orgID, nodeID uuid.UUID, userIDs []uuid.UUID, assignedBy uuid.UUID) error {
	existing, err := s.ListByNode(context.Background(), orgID, nodeID)
	if err != nil {
		return fmt.Errorf("assignment set all: %w", err)
	}
	val, _ := json.Marshal(AssignmentValue{AssignedBy: assignedBy, AssignedAt: time.Now().UTC()})
	_, err = s.db.Transact(func(tr fdb.Transaction) (any, error) {
		org := orgID.String()
		node := nodeID.String()
		// Clear existing
		for _, uid := range existing {
			tr.Clear(fdb.Key(tuple.Tuple{keyAssignmentOnNode, org, node, uid.String()}.Pack()))
		}
		// Write new
		for _, uid := range userIDs {
			tr.Set(fdb.Key(tuple.Tuple{keyAssignmentOnNode, org, node, uid.String()}.Pack()), val)
			tr.Set(fdb.Key(tuple.Tuple{keyAssignmentToUser, org, uid.String(), time.Now().UnixNano(), node}.Pack()), nil)
		}
		return nil, nil
	})
	return err
}

// ListByNode returns all userIDs assigned to a node.
func (s *AssignmentStore) ListByNode(_ context.Context, orgID, nodeID uuid.UUID) ([]uuid.UUID, error) {
	pr, _ := fdb.PrefixRange(tuple.Tuple{keyAssignmentOnNode, orgID.String(), nodeID.String()}.Pack())
	vals, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.GetRange(pr, fdb.RangeOptions{}).GetSliceWithError()
	})
	if err != nil {
		return nil, fmt.Errorf("assignment list by node: %w", err)
	}
	return extractLastUUIDs(vals.([]fdb.KeyValue), 3), nil
}

// ListByUser returns all nodeIDs assigned to a user, newest first.
func (s *AssignmentStore) ListByUser(_ context.Context, orgID, userID uuid.UUID) ([]uuid.UUID, error) {
	pr, _ := fdb.PrefixRange(tuple.Tuple{keyAssignmentToUser, orgID.String(), userID.String()}.Pack())
	vals, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.GetRange(pr, fdb.RangeOptions{Reverse: true}).GetSliceWithError()
	})
	if err != nil {
		return nil, fmt.Errorf("assignment list by user: %w", err)
	}
	return extractLastUUIDs(vals.([]fdb.KeyValue), 4), nil
}
