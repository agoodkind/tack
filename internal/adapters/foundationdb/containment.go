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

// ContainmentStore implements node.ContainmentRepository.
type ContainmentStore struct{ db fdb.Database }

func newContainmentStore(db fdb.Database) *ContainmentStore { return &ContainmentStore{db: db} }

func (s *ContainmentStore) AddIssueToModule(_ context.Context, orgID, moduleID, issueID, addedBy uuid.UUID) error {
	return s.addContainment(keyIssueInModule, keyModulesContainingIssue, orgID, moduleID, issueID, addedBy)
}

func (s *ContainmentStore) RemoveIssueFromModule(_ context.Context, orgID, moduleID, issueID uuid.UUID) error {
	return s.removeContainment(keyIssueInModule, keyModulesContainingIssue, orgID, moduleID, issueID)
}

func (s *ContainmentStore) IssuesInModule(_ context.Context, orgID, moduleID uuid.UUID) ([]uuid.UUID, error) {
	return s.listContained(keyIssueInModule, orgID, moduleID)
}

func (s *ContainmentStore) ModulesContainingIssue(_ context.Context, orgID, issueID uuid.UUID) ([]uuid.UUID, error) {
	return s.listContained(keyModulesContainingIssue, orgID, issueID)
}

func (s *ContainmentStore) AddIssueToCycle(_ context.Context, orgID, cycleID, issueID, addedBy uuid.UUID) error {
	return s.addContainment(keyIssueInCycle, keyCyclesContainingIssue, orgID, cycleID, issueID, addedBy)
}

func (s *ContainmentStore) RemoveIssueFromCycle(_ context.Context, orgID, cycleID, issueID uuid.UUID) error {
	return s.removeContainment(keyIssueInCycle, keyCyclesContainingIssue, orgID, cycleID, issueID)
}

func (s *ContainmentStore) IssuesInCycle(_ context.Context, orgID, cycleID uuid.UUID) ([]uuid.UUID, error) {
	return s.listContained(keyIssueInCycle, orgID, cycleID)
}

func (s *ContainmentStore) CyclesContainingIssue(_ context.Context, orgID, issueID uuid.UUID) ([]uuid.UUID, error) {
	return s.listContained(keyCyclesContainingIssue, orgID, issueID)
}

func (s *ContainmentStore) IssuesInEpic(_ context.Context, orgID, epicID uuid.UUID) ([]uuid.UUID, error) {
	return s.listContained(keyIssuesInEpic, orgID, epicID)
}

func (s *ContainmentStore) EpicsContainingIssue(_ context.Context, orgID, issueID uuid.UUID) (*uuid.UUID, error) {
	// issues_in_epic is keyed (keyIssuesInEpic, orgID, epicID, issueID).
	// We need the reverse: given issueID, find epicID.
	// Use a reverse index: (keyIssueEpicReverse, orgID, issueID, epicID) -> nil
	// For now we do a prefix scan of the forward index is not viable; instead we
	// store a reverse entry. Key: (keyIssueEpicReverse, orgID, issueID) -> epicID bytes.
	key := fdb.Key(tuple.Tuple{keyIssueEpicReverse, orgID.String(), issueID.String()}.Pack())
	val, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.Get(key).Get()
	})
	if err != nil {
		return nil, fmt.Errorf("epic for issue: %w", err)
	}
	b, ok := val.([]byte)
	if !ok || len(b) < 16 {
		return nil, nil
	}
	id, err := uuid.FromBytes(b[:16])
	if err != nil {
		return nil, nil
	}
	return &id, nil
}

func (s *ContainmentStore) addContainment(fwdKey, revKey string, orgID, containerID, itemID, addedBy uuid.UUID) error {
	val, _ := json.Marshal(ContainmentValue{AddedBy: addedBy, AddedAt: time.Now().UTC()})
	_, err := s.db.Transact(func(tr fdb.Transaction) (any, error) {
		org := orgID.String()
		tr.Set(fdb.Key(tuple.Tuple{fwdKey, org, containerID.String(), itemID.String()}.Pack()), val)
		tr.Set(fdb.Key(tuple.Tuple{revKey, org, itemID.String(), containerID.String()}.Pack()), nil)
		return nil, nil
	})
	return err
}

func (s *ContainmentStore) removeContainment(fwdKey, revKey string, orgID, containerID, itemID uuid.UUID) error {
	_, err := s.db.Transact(func(tr fdb.Transaction) (any, error) {
		org := orgID.String()
		tr.Clear(fdb.Key(tuple.Tuple{fwdKey, org, containerID.String(), itemID.String()}.Pack()))
		tr.Clear(fdb.Key(tuple.Tuple{revKey, org, itemID.String(), containerID.String()}.Pack()))
		return nil, nil
	})
	return err
}

func (s *ContainmentStore) listContained(prefix string, orgID, containerID uuid.UUID) ([]uuid.UUID, error) {
	pr, _ := fdb.PrefixRange(tuple.Tuple{prefix, orgID.String(), containerID.String()}.Pack())
	vals, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.GetRange(pr, fdb.RangeOptions{}).GetSliceWithError()
	})
	if err != nil {
		return nil, fmt.Errorf("containment list: %w", err)
	}
	return extractLastUUIDs(vals.([]fdb.KeyValue), 3), nil
}
