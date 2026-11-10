package search

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
)

var (
	// ErrSessionChanged means another call committed the cursor's session
	// version first. The caller replays the committed page instead.
	ErrSessionChanged = errors.New("search session changed")
	// ErrSessionExpired means the idle or absolute deadline passed.
	ErrSessionExpired = errors.New("search session expired")
	// ErrSessionNotFound means the cursor does not identify an existing session.
	ErrSessionNotFound = errors.New("search session was not found")
	// ErrSessionMismatch means the request no longer matches the session binding.
	ErrSessionMismatch = errors.New("search request does not match its session")
	// ErrSessionStepNotStarted means cleanup did not attempt a transaction
	// because its request context had ended.
	ErrSessionStepNotStarted = errors.New("search session step did not start")
)

// Session is the durable continuation state of one search. FoundationDB
// stores it, and every Tack process reads it. Version counts committed
// responses.
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

// Expired reports whether a session deadline has passed at now.
//
// The method checks both the idle and absolute deadlines.
//
//   - A time equal to or after either deadline returns true.
//   - A time before both deadlines returns false.
func (s Session) Expired(now time.Time) bool {
	return !now.Before(s.IdleDeadline) || !now.Before(s.AbsoluteDeadline)
}

// Continuable reports whether the session can serve another page at now.
//
// The session binding includes the restore epoch. A session from an earlier
// epoch fails continuation with [ErrSessionMismatch].
//
//   - A closing or expired session cannot serve another page.
//   - The query generation must equal restoreEpoch.
func (s Session) Continuable(now time.Time, restoreEpoch int64) bool {
	return !s.Closing && s.Query.Generation == restoreEpoch && !s.Expired(now)
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

// CleanupStep identifies a failed session cleanup step and supplies its
// default audit failure code. The store uses another code when the step
// does not attempt a transaction or the commit result remains uncertain.
type CleanupStep string

const (
	// CleanupClose is the step that marks a session closing.
	CleanupClose CleanupStep = "session_close_failed"
	// CleanupDelete is the step that loads a session for deletion or deletes
	// its records.
	CleanupDelete CleanupStep = "session_delete_failed"
)

// SessionStore persists sessions, visited nodes, and replay records.
type SessionStore interface {
	Create(context.Context, Session) (Session, error)
	Load(context.Context, uuid.UUID) (Session, error)
	Replay(context.Context, uuid.UUID, uint64) (PageCommit, bool, error)
	HasVisited(context.Context, uuid.UUID, []uuid.UUID) (map[uuid.UUID]bool, error)
	CommitPage(context.Context, uuid.UUID, uint64, PageCommit) (Session, error)
	BeginCleanup(context.Context, uuid.UUID) error
	CleanupSlice(context.Context, uuid.UUID, int) (bool, error)
	RecordCleanupFailure(context.Context, uuid.UUID, CleanupStep, error) error
}

// RankingVersion identifies the scoring formula for ranked page requests.
// Its current value is "2". SessionBinding hashes it with the other session
// identity values. A different RankingVersion causes continuation and replay
// to fail with ErrSessionMismatch. A scoring change requires a new
// RankingVersion.
const RankingVersion = "2"

// SessionBinding hashes the session identity with the current ranking version.
//
// The function calls [RankedSessionBinding] with [RankingVersion].
//
//   - The arguments supply the principal, authority, entry point, and query.
func SessionBinding(
	principalID uuid.UUID,
	authorityID uuid.UUID,
	entryPointID uuid.UUID,
	query Query,
) [sha256.Size]byte {
	return RankedSessionBinding(RankingVersion, principalID, authorityID, entryPointID, query)
}

// RankedSessionBinding hashes a session identity with length-prefixed
// serialization.
//
// The ranking version and restore epoch distinguish incompatible sessions.
//
//   - The identity includes principal, permission authority, and entry point.
//   - Query identity includes text, node type, index, and access version.
//   - Each access key contributes a separate length-prefixed value.
func RankedSessionBinding(
	rankingVersion string,
	principalID uuid.UUID,
	authorityID uuid.UUID,
	entryPointID uuid.UUID,
	query Query,
) [sha256.Size]byte {
	var identity bytes.Buffer
	writeIdentityPart(&identity, []byte(rankingVersion))
	writeIdentityPart(&identity, []byte(strconv.FormatInt(query.Generation, 10)))
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
