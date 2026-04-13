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

type ContainmentValue struct {
	AddedBy uuid.UUID `json:"added_by"`
	AddedAt time.Time `json:"added_at"`
}

// ContainmentStore implements node.ContainmentRepository with a single generic
// key pattern: (containment, orgID, containerID, childID) for the forward index,
// (containment_reverse, orgID, childID, containerID) for the reverse.
type ContainmentStore struct{ db fdb.Database }

func newContainmentStore(db fdb.Database) *ContainmentStore { return &ContainmentStore{db: db} }

func (s *ContainmentStore) AddChild(_ context.Context, orgID, containerID, childID, addedBy uuid.UUID) error {
	val, _ := json.Marshal(ContainmentValue{AddedBy: addedBy, AddedAt: time.Now().UTC()})
	_, err := s.db.Transact(func(tr fdb.Transaction) (any, error) {
		org := orgID.String()
		tr.Set(fdb.Key(tuple.Tuple{keyContainment, org, containerID.String(), childID.String()}.Pack()), val)
		tr.Set(fdb.Key(tuple.Tuple{keyContainmentReverse, org, childID.String(), containerID.String()}.Pack()), nil)
		return nil, nil
	})
	return err
}

func (s *ContainmentStore) RemoveChild(_ context.Context, orgID, containerID, childID uuid.UUID) error {
	_, err := s.db.Transact(func(tr fdb.Transaction) (any, error) {
		org := orgID.String()
		tr.Clear(fdb.Key(tuple.Tuple{keyContainment, org, containerID.String(), childID.String()}.Pack()))
		tr.Clear(fdb.Key(tuple.Tuple{keyContainmentReverse, org, childID.String(), containerID.String()}.Pack()))
		return nil, nil
	})
	return err
}

func (s *ContainmentStore) ListChildren(_ context.Context, orgID, containerID uuid.UUID) ([]uuid.UUID, error) {
	pr, _ := fdb.PrefixRange(tuple.Tuple{keyContainment, orgID.String(), containerID.String()}.Pack())
	vals, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.GetRange(pr, fdb.RangeOptions{}).GetSliceWithError()
	})
	if err != nil {
		return nil, fmt.Errorf("containment list children: %w", err)
	}
	return extractLastUUIDs(vals.([]fdb.KeyValue), 3), nil
}

func (s *ContainmentStore) ListContainers(_ context.Context, orgID, childID uuid.UUID) ([]uuid.UUID, error) {
	pr, _ := fdb.PrefixRange(tuple.Tuple{keyContainmentReverse, orgID.String(), childID.String()}.Pack())
	vals, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.GetRange(pr, fdb.RangeOptions{}).GetSliceWithError()
	})
	if err != nil {
		return nil, fmt.Errorf("containment list containers: %w", err)
	}
	return extractLastUUIDs(vals.([]fdb.KeyValue), 3), nil
}
