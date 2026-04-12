package foundationdb

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
)

type MembershipValue struct {
	Role    string    `json:"role"`
	AddedBy uuid.UUID `json:"added_by"`
	AddedAt time.Time `json:"added_at"`
}

// MembershipStore manages sub-org membership (workspace, project, team, custom nodes).
// org_members stays in SQL as the auth gate.
type MembershipStore struct{ db fdb.Database }

func newMembershipStore(db fdb.Database) *MembershipStore { return &MembershipStore{db: db} }

// Set adds or updates a membership. Writes all three index directions atomically.
func (s *MembershipStore) Set(orgID, userID uuid.UUID, entityType string, entityID uuid.UUID, role string, addedBy uuid.UUID) error {
	val, _ := json.Marshal(MembershipValue{Role: role, AddedBy: addedBy, AddedAt: time.Now().UTC()})
	_, err := s.db.Transact(func(tr fdb.Transaction) (any, error) {
		org, user, ent := orgID.String(), userID.String(), entityID.String()
		tr.Set(fdb.Key(tuple.Tuple{keyMembershipByUser, org, user, entityType, ent}.Pack()), val)
		tr.Set(fdb.Key(tuple.Tuple{keyMembershipByEntity, org, entityType, ent, user}.Pack()), val)
		tr.Set(fdb.Key(tuple.Tuple{keyMembershipByRole, org, entityType, ent, role, user}.Pack()), nil)
		return nil, nil
	})
	return err
}

// Delete removes a membership. Requires the current role to clear the role index.
func (s *MembershipStore) Delete(orgID, userID uuid.UUID, entityType string, entityID uuid.UUID) error {
	m, err := s.Get(orgID, userID, entityType, entityID)
	if err != nil {
		return err
	}
	role := ""
	if m != nil {
		role = m.Role
	}
	_, err = s.db.Transact(func(tr fdb.Transaction) (any, error) {
		org, user, ent := orgID.String(), userID.String(), entityID.String()
		tr.Clear(fdb.Key(tuple.Tuple{keyMembershipByUser, org, user, entityType, ent}.Pack()))
		tr.Clear(fdb.Key(tuple.Tuple{keyMembershipByEntity, org, entityType, ent, user}.Pack()))
		if role != "" {
			tr.Clear(fdb.Key(tuple.Tuple{keyMembershipByRole, org, entityType, ent, role, user}.Pack()))
		}
		return nil, nil
	})
	return err
}

// Get returns the membership for a specific user+entity, or nil if not found.
func (s *MembershipStore) Get(orgID, userID uuid.UUID, entityType string, entityID uuid.UUID) (*MembershipValue, error) {
	key := tuple.Tuple{keyMembershipByUser, orgID.String(), userID.String(), entityType, entityID.String()}.Pack()
	val, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.Get(fdb.Key(key)).Get()
	})
	if err != nil {
		return nil, fmt.Errorf("membership get: %w", err)
	}
	b, ok := val.([]byte)
	if !ok || len(b) == 0 {
		return nil, nil
	}
	var m MembershipValue
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// ListByUser returns all (entityType, entityID, role) for a user in an org.
func (s *MembershipStore) ListByUser(orgID, userID uuid.UUID) ([]EntityMembership, error) {
	pr, _ := fdb.PrefixRange(tuple.Tuple{keyMembershipByUser, orgID.String(), userID.String()}.Pack())
	return s.scanMemberships(pr, 3)
}

// ListByEntity returns all (userID, role) members of an entity.
func (s *MembershipStore) ListByEntity(orgID uuid.UUID, entityType string, entityID uuid.UUID) ([]UserMembership, error) {
	pr, _ := fdb.PrefixRange(tuple.Tuple{keyMembershipByEntity, orgID.String(), entityType, entityID.String()}.Pack())
	vals, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.GetRange(pr, fdb.RangeOptions{}).GetSliceWithError()
	})
	if err != nil {
		return nil, fmt.Errorf("membership list by entity: %w", err)
	}
	kvs := vals.([]fdb.KeyValue)
	result := make([]UserMembership, 0, len(kvs))
	for _, kv := range kvs {
		t, err := tuple.Unpack(kv.Key)
		if err != nil || len(t) < 5 {
			continue
		}
		uid, err := uuid.Parse(t[4].(string))
		if err != nil {
			continue
		}
		var m MembershipValue
		if err := json.Unmarshal(kv.Value, &m); err != nil {
			continue
		}
		result = append(result, UserMembership{UserID: uid, Role: m.Role})
	}
	return result, nil
}

// ListByRole returns all userIDs with a given role on an entity.
func (s *MembershipStore) ListByRole(orgID uuid.UUID, entityType string, entityID uuid.UUID, role string) ([]uuid.UUID, error) {
	pr, _ := fdb.PrefixRange(tuple.Tuple{keyMembershipByRole, orgID.String(), entityType, entityID.String(), role}.Pack())
	vals, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.GetRange(pr, fdb.RangeOptions{}).GetSliceWithError()
	})
	if err != nil {
		return nil, fmt.Errorf("membership list by role: %w", err)
	}
	kvs := vals.([]fdb.KeyValue)
	ids := make([]uuid.UUID, 0, len(kvs))
	for _, kv := range kvs {
		t, err := tuple.Unpack(kv.Key)
		if err != nil || len(t) < 6 {
			continue
		}
		id, err := uuid.Parse(t[5].(string))
		if err != nil {
			continue
		}
		ids = append(ids, id)
	}
	return ids, nil
}

type EntityMembership struct {
	EntityType string
	EntityID   uuid.UUID
	Role       string
}

type UserMembership struct {
	UserID uuid.UUID
	Role   string
}

func (s *MembershipStore) scanMemberships(pr fdb.KeyRange, entityIDIdx int) ([]EntityMembership, error) {
	vals, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.GetRange(pr, fdb.RangeOptions{}).GetSliceWithError()
	})
	if err != nil {
		return nil, fmt.Errorf("membership scan: %w", err)
	}
	kvs := vals.([]fdb.KeyValue)
	result := make([]EntityMembership, 0, len(kvs))
	for _, kv := range kvs {
		t, err := tuple.Unpack(kv.Key)
		if err != nil || len(t) < entityIDIdx+2 {
			continue
		}
		entityType, ok1 := t[entityIDIdx].(string)
		entityIDStr, ok2 := t[entityIDIdx+1].(string)
		if !ok1 || !ok2 {
			continue
		}
		entityID, err := uuid.Parse(entityIDStr)
		if err != nil {
			continue
		}
		var m MembershipValue
		if err := json.Unmarshal(kv.Value, &m); err != nil {
			continue
		}
		result = append(result, EntityMembership{EntityType: entityType, EntityID: entityID, Role: m.Role})
	}
	return result, nil
}
