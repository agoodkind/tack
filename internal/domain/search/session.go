package search

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	// ErrSessionChanged means another call committed the cursor's session
	// version first. The caller replays the committed page instead.
	ErrSessionChanged = errors.New("search session changed")
	// ErrSessionExpired means the idle or absolute deadline passed.
	ErrSessionExpired = errors.New("search session expired")
	// ErrSessionNotFound means no session exists for the cursor.
	ErrSessionNotFound = errors.New("search session was not found")
	// ErrSessionMismatch means the request no longer matches the session binding.
	ErrSessionMismatch = errors.New("search request does not match its session")
)

// Session is the durable continuation state every Tack process shares
// through FoundationDB. Version counts committed responses.
type Session struct {
	ID, PrincipalID, AuthorityID, EntryPointID uuid.UUID
	Binding                                    [sha256.Size]byte
	Query                                      Query
	Snapshot                                   Snapshot
	// Sort is the exact raw sort array of the last consumed page match.
	Sort                                      json.RawMessage
	Version                                   uint64
	CreatedAt, IdleDeadline, AbsoluteDeadline time.Time
	Complete, Closing                         bool
}

// Expired reports whether either deadline passed at now.
func (s Session) Expired(now time.Time) bool {
	return !now.Before(s.IdleDeadline) || !now.Before(s.AbsoluteDeadline)
}

// PageCommit is the exact consumed position and bounded response of one
// public page. The replay record stores ResultIDs and Complete.
type PageCommit struct {
	Sort      json.RawMessage
	PITID     string
	Visited   []uuid.UUID
	ResultIDs []uuid.UUID
	Complete  bool
}

// SessionStore persists sessions, visited nodes, and replay records.
type SessionStore interface {
	Create(context.Context, Session) (Session, error)
	Load(context.Context, uuid.UUID) (Session, error)
	Replay(context.Context, uuid.UUID, uint64) (PageCommit, bool, error)
	HasVisited(context.Context, uuid.UUID, []uuid.UUID) (map[uuid.UUID]bool, error)
	CommitPage(context.Context, uuid.UUID, uint64, PageCommit) (Session, error)
	BeginCleanup(context.Context, uuid.UUID) error
	CleanupSlice(context.Context, uuid.UUID, int) (bool, error)
}

// SessionBinding hashes the principal, permission authority, entry point,
// query text, node type, access version and keys, and physical index through
// length-prefixed serialization.
func SessionBinding(principalID, authorityID, entryPointID uuid.UUID, query Query) [sha256.Size]byte {
	var identity bytes.Buffer
	writeIdentityPart(&identity, principalID[:])
	writeIdentityPart(&identity, authorityID[:])
	writeIdentityPart(&identity, entryPointID[:])
	writeIdentityPart(&identity, []byte(query.Text))
	writeIdentityPart(&identity, []byte(query.NodeType))
	writeIdentityPart(&identity, []byte(query.Index))
	writeIdentityPart(&identity, []byte(query.Access.Version))
	for _, key := range query.Access.Keys {
		writeIdentityPart(&identity, []byte(key))
	}
	return sha256.Sum256(identity.Bytes())
}
