package foundationdb

import (
	"bytes"
	"encoding/json"
	"slices"

	"goodkind.io/tack/internal/audit"
)

func (a *sessionAudit) recordSessionCreate(state sessionState) error {
	after, protected, err := a.completeSessionDelta(state, sessionSideNew)
	if err != nil {
		return err
	}
	target := audit.SearchSessionTarget(state.record.ID, state.record.PrincipalID)
	return a.recordProtected(target, nil, after, protected)
}

// recordSessionDelete records a removed session with every member that it
// still stores.
func (a *sessionAudit) recordSessionDelete(state sessionState) error {
	before, protected, err := a.completeSessionDelta(state, sessionSideOld)
	if err != nil {
		return err
	}
	target := audit.SearchSessionTarget(state.record.ID, state.record.PrincipalID)
	return a.recordProtected(target, before, nil, protected)
}

// recordSessionUpdate records the identity members and every changed member
// with its actual old and new value. A member that changes from or to absent
// appears on one side of the delta.
func (a *sessionAudit) recordSessionUpdate(previous, current sessionState) error {
	oldPlain, err := plainSessionMembers(previous.record)
	if err != nil {
		return err
	}
	newPlain, err := plainSessionMembers(current.record)
	if err != nil {
		return err
	}
	before, after := map[string]json.RawMessage{}, map[string]json.RawMessage{}
	for member, value := range oldPlain {
		if slices.Contains(sessionIdentityMembers, member) || !bytes.Equal(value, newPlain[member]) {
			before[member], after[member] = value, newPlain[member]
		}
	}
	protected, err := a.changedProtected(previous, current, before, after)
	if err != nil {
		return err
	}
	encodedBefore, err := encodeSessionValue(before)
	if err != nil {
		return err
	}
	encodedAfter, err := encodeSessionValue(after)
	if err != nil {
		return err
	}
	target := audit.SearchSessionTarget(current.record.ID, current.record.PrincipalID)
	return a.recordProtected(target, encodedBefore, encodedAfter, protected)
}

// changedProtected adds a reference to before and after for each protected
// member that differs between previous and current.
func (a *sessionAudit) changedProtected(previous, current sessionState, before, after map[string]json.RawMessage) ([]audit.ProtectedValue, error) {
	oldValues, err := protectedSessionMembers(previous)
	if err != nil {
		return nil, err
	}
	newValues, err := protectedSessionMembers(current)
	if err != nil {
		return nil, err
	}
	for _, member := range sessionProtectedMembers {
		if bytes.Equal(oldValues[member], newValues[member]) {
			delete(oldValues, member)
			delete(newValues, member)
		}
	}
	subject := current.record.PrincipalID
	oldProtected, err := a.protectAll(before, oldValues, sessionSideOld, subject)
	if err != nil {
		return nil, err
	}
	newProtected, err := a.protectAll(after, newValues, sessionSideNew, subject)
	if err != nil {
		return nil, err
	}
	return append(oldProtected, newProtected...), nil
}
