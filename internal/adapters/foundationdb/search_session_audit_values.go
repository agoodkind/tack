package foundationdb

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

const (
	sessionMemberText       = "text"
	sessionMemberTokens     = "query_tokens"
	sessionMemberSort       = "sort"
	sessionMemberAccessKeys = "access_keys"
	sessionSideOld          = "old"
	sessionSideNew          = "new"
)

// This order determines the order of protected values in the event.
var sessionProtectedMembers = []string{
	sessionMemberText,
	sessionMemberTokens,
	sessionMemberSort,
	sessionMemberAccessKeys,
}

var sessionIdentityMembers = []string{"id", "principal_id", "authority_id", "entry_point_id"}

// sessionPlainValue is the part of a session audit value that the ledger row
// stores directly. VersionEntry reports whether the version presence entry
// exists. The idle_deadline member covers the expiry entry, and the index
// member covers the presence entry.
type sessionPlainValue struct {
	ID               uuid.UUID `json:"id"`
	PrincipalID      uuid.UUID `json:"principal_id"`
	AuthorityID      uuid.UUID `json:"authority_id"`
	EntryPointID     uuid.UUID `json:"entry_point_id"`
	Binding          string    `json:"binding"`
	Index            string    `json:"index"`
	NodeType         string    `json:"node_type"`
	AccessVersion    string    `json:"access_version"`
	Generation       int64     `json:"generation"`
	PITID            string    `json:"pit_id"`
	Version          uint64    `json:"version"`
	TokenBytes       int       `json:"token_bytes"`
	TokenChunks      int       `json:"token_chunks"`
	CreatedAt        time.Time `json:"created_at"`
	IdleDeadline     time.Time `json:"idle_deadline"`
	AbsoluteDeadline time.Time `json:"absolute_deadline"`
	Complete         bool      `json:"complete"`
	Closing          bool      `json:"closing"`
	VersionEntry     bool      `json:"version_entry"`
}

func plainSessionMembers(record searchSessionRecord) (map[string]json.RawMessage, error) {
	encoded, err := encodeSessionValue(sessionPlainValue{
		ID:               record.ID,
		PrincipalID:      record.PrincipalID,
		AuthorityID:      record.AuthorityID,
		EntryPointID:     record.EntryPointID,
		Binding:          record.Binding,
		Index:            record.Index,
		NodeType:         record.NodeType,
		AccessVersion:    record.AccessVersion,
		Generation:       record.Generation,
		PITID:            record.PITID,
		Version:          record.Version,
		TokenBytes:       record.TokenBytes,
		TokenChunks:      record.TokenChunks,
		CreatedAt:        record.CreatedAt,
		IdleDeadline:     record.IdleDeadline,
		AbsoluteDeadline: record.AbsoluteDeadline,
		Complete:         record.Complete,
		Closing:          record.Closing,
		VersionEntry:     !record.Complete && !record.Closing,
	})
	if err != nil {
		return nil, err
	}

	members := map[string]json.RawMessage{}
	if err := json.Unmarshal(encoded, &members); err != nil {
		return nil, sessionStepError{operation: "decode search session audit members", err: err}
	}

	return members, nil
}

// protectedSessionMembers returns the raw JSON value of each protected member
// of state. Text and access keys always exist. Sort exists after the first
// committed position, and query_tokens exists when state.tokens is not empty.
func protectedSessionMembers(state sessionState) (map[string]json.RawMessage, error) {
	text, err := encodeSessionValue(state.record.Text)
	if err != nil {
		return nil, err
	}

	keys, err := encodeSessionValue(state.record.AccessKeys)
	if err != nil {
		return nil, err
	}

	members := map[string]json.RawMessage{sessionMemberText: text, sessionMemberAccessKeys: keys}
	if len(state.record.Sort) > 0 {
		members[sessionMemberSort] = state.record.Sort
	}

	if len(state.tokens) > 0 {
		tokens, err := protectedTokens(state.tokens)
		if err != nil {
			return nil, err
		}

		members[sessionMemberTokens] = tokens
	}

	return members, nil
}

// protectedTokens returns the stored bytes as a JSON value. It encodes bytes
// that do not form valid JSON as a JSON string. Protected values require
// valid JSON.
func protectedTokens(stored []byte) (json.RawMessage, error) {
	if json.Valid(stored) {
		return stored, nil
	}

	return encodeSessionValue(string(stored))
}

// protect stores a reference for one protected member in members and returns
// the value that audit.pii stores under the subject of the session.
func (a *sessionAudit) protect(
	members map[string]json.RawMessage,
	side string,
	member string,
	value json.RawMessage,
	subject uuid.UUID,
) (audit.ProtectedValue, error) {
	reference := uuid.NewSHA1(a.base, []byte(side+":"+member))
	encoded, err := encodeSessionValue(audit.ProtectedReference{PIIRef: reference})
	if err != nil {
		return audit.ProtectedValue{}, err
	}

	members[member] = encoded

	return audit.ProtectedValue{Ref: reference, SubjectID: subject, Value: value}, nil
}

func (a *sessionAudit) protectAll(
	members map[string]json.RawMessage,
	values map[string]json.RawMessage,
	side string,
	subject uuid.UUID,
) ([]audit.ProtectedValue, error) {
	protected := make([]audit.ProtectedValue, 0, len(values))

	for _, member := range sessionProtectedMembers {
		value, found := values[member]
		if !found {
			continue
		}

		stored, err := a.protect(members, side, member, value, subject)
		if err != nil {
			return nil, err
		}

		protected = append(protected, stored)
	}

	return protected, nil
}

func (a *sessionAudit) completeSessionDelta(
	state sessionState,
	side string,
) (json.RawMessage, []audit.ProtectedValue, error) {
	members, err := plainSessionMembers(state.record)
	if err != nil {
		return nil, nil, err
	}

	values, err := protectedSessionMembers(state)
	if err != nil {
		return nil, nil, err
	}

	protected, err := a.protectAll(members, values, side, state.record.PrincipalID)
	if err != nil {
		return nil, nil, err
	}

	encoded, err := encodeSessionValue(members)

	return encoded, protected, err
}
