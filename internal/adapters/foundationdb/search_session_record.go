package foundationdb

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

const (
	// maxSessionHeaderBytes bounds one encoded session header.
	maxSessionHeaderBytes = 64 << 10
	// sessionTokenChunkBytes bounds one stored chunk of query-token bytes.
	sessionTokenChunkBytes = 16 << 10
	// maxSessionVisitedIDs bounds the visited IDs one page commit writes.
	maxSessionVisitedIDs = 400
	// maxSessionResultIDs bounds the result IDs one replay record stores.
	maxSessionResultIDs = 25
)

// searchSessionRecord is the bounded session header. Query tokens are stored
// in separate chunk keys.
type searchSessionRecord struct {
	ID               uuid.UUID       `json:"id"`
	PrincipalID      uuid.UUID       `json:"principal_id"`
	AuthorityID      uuid.UUID       `json:"authority_id"`
	EntryPointID     uuid.UUID       `json:"entry_point_id"`
	Binding          string          `json:"binding"`
	Text             string          `json:"text"`
	Index            string          `json:"index"`
	NodeType         string          `json:"node_type"`
	AccessVersion    string          `json:"access_version"`
	AccessKeys       []string        `json:"access_keys"`
	Generation       int64           `json:"generation"`
	PITID            string          `json:"pit_id"`
	Sort             json.RawMessage `json:"sort,omitempty"`
	Version          uint64          `json:"version"`
	TokenBytes       int             `json:"token_bytes"`
	TokenChunks      int             `json:"token_chunks"`
	CreatedAt        time.Time       `json:"created_at"`
	IdleDeadline     time.Time       `json:"idle_deadline"`
	AbsoluteDeadline time.Time       `json:"absolute_deadline"`
	Complete         bool            `json:"complete"`
	Closing          bool            `json:"closing"`
}

// searchReplayRecord stores the result IDs and the completion flag that one
// session version committed.
type searchReplayRecord struct {
	ResultIDs []uuid.UUID `json:"result_ids"`
	Complete  bool        `json:"complete"`
}

func sessionRecordFor(session searchdomain.Session) searchSessionRecord {
	chunks := (len(session.Snapshot.QueryTokens) + sessionTokenChunkBytes - 1) / sessionTokenChunkBytes
	return searchSessionRecord{
		ID: session.ID, PrincipalID: session.PrincipalID, AuthorityID: session.AuthorityID,
		EntryPointID: session.EntryPointID, Binding: hex.EncodeToString(session.Binding[:]),
		Text: session.Query.Text, Index: session.Query.Index, NodeType: session.Query.NodeType,
		AccessVersion: session.Query.Access.Version, AccessKeys: session.Query.Access.Keys,
		Generation: session.Query.Generation, PITID: session.Snapshot.PITID, Sort: session.Sort, Version: session.Version,
		TokenBytes: len(session.Snapshot.QueryTokens), TokenChunks: chunks,
		CreatedAt: session.CreatedAt, IdleDeadline: session.IdleDeadline, AbsoluteDeadline: session.AbsoluteDeadline,
		Complete: session.Complete, Closing: session.Closing,
	}
}

func (r searchSessionRecord) session(tokens json.RawMessage) (searchdomain.Session, error) {
	var binding [32]byte
	decoded, err := hex.DecodeString(r.Binding)
	if err != nil || len(decoded) != len(binding) {
		return searchdomain.Session{}, fmt.Errorf("session %s binding is invalid", r.ID)
	}
	copy(binding[:], decoded)
	query := searchdomain.Query{
		Text: r.Text, Index: r.Index, NodeType: r.NodeType,
		Access:     searchdomain.AccessFilter{Version: r.AccessVersion, Keys: r.AccessKeys},
		Generation: r.Generation,
	}
	return searchdomain.Session{
		ID: r.ID, PrincipalID: r.PrincipalID, AuthorityID: r.AuthorityID, EntryPointID: r.EntryPointID,
		Binding: binding, Query: query,
		Snapshot: searchdomain.Snapshot{PITID: r.PITID, Index: r.Index, QueryTokens: tokens},
		Sort:     r.Sort, Version: r.Version, CreatedAt: r.CreatedAt, IdleDeadline: r.IdleDeadline,
		AbsoluteDeadline: r.AbsoluteDeadline, Complete: r.Complete, Closing: r.Closing,
	}, nil
}

type sessionValue interface {
	searchSessionRecord | searchReplayRecord
}

func readSessionValue[Value sessionValue](tr fdb.ReadTransaction, key []byte, value *Value) (bool, error) {
	encoded, err := tr.Get(fdb.Key(key)).Get()
	if err != nil {
		return false, sessionStepError{operation: "read search session value", err: err}
	}
	if len(encoded) == 0 {
		return false, nil
	}
	if err := json.Unmarshal(encoded, value); err != nil {
		return false, sessionStepError{operation: "decode search session value", err: err}
	}
	return true, nil
}

func writeSessionValue[Value sessionValue](tr fdb.Transaction, key []byte, value Value, limit int) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return sessionStepError{operation: "encode search session value", err: err}
	}
	if len(encoded) > limit {
		operation := fmt.Sprintf("search session value contains %d bytes, above the %d-byte bound", len(encoded), limit)
		return sessionStepError{operation: operation, err: searchdomain.ErrInvalidQuery}
	}
	tr.Set(fdb.Key(key), encoded)
	return nil
}

// sessionStepError adds operation context to a session step failure. The
// transaction boundary logs it once.
type sessionStepError struct {
	operation string
	err       error
}

func (e sessionStepError) Error() string { return e.operation + ": " + e.err.Error() }
func (e sessionStepError) Unwrap() error { return e.err }

// isSessionOutcome reports whether err is an expected session state rather
// than a storage failure.
func isSessionOutcome(err error) bool {
	return errors.Is(err, searchdomain.ErrSessionChanged) || errors.Is(err, searchdomain.ErrSessionExpired) ||
		errors.Is(err, searchdomain.ErrSessionNotFound) || errors.Is(err, searchdomain.ErrInvalidQuery)
}
