package foundationdb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// SearchSessionStore persists query sessions in FoundationDB. It reads the
// current time from its clock to check and renew session deadlines.
type SearchSessionStore struct {
	db          fdb.Database
	clock       clock.Clock
	idleTimeout time.Duration
}

var _ searchdomain.SessionStore = (*SearchSessionStore)(nil)

// NewSearchSessionStore creates the FoundationDB session adapter. Each page
// commit reads the clock inside its transaction. It sets the idle deadline
// to idleTimeout after that time or to the absolute deadline, whichever is
// earlier.
func NewSearchSessionStore(db fdb.Database, source clock.Clock, idleTimeout time.Duration) *SearchSessionStore {
	return &SearchSessionStore{db: db, clock: source, idleTimeout: idleTimeout}
}

// Create stores the session header, the chunked query tokens, and the expiry,
// presence, and version presence keys in one transaction. It refuses a
// session under an access version that the authority no longer writes.
func (s *SearchSessionStore) Create(ctx context.Context, session searchdomain.Session) (created searchdomain.Session, err error) {
	defer telemetry.FDBOp(ctx, "store.search_session.create")(&err)
	if session.ID == uuid.Nil || session.Version != 0 || len(session.Snapshot.QueryTokens) == 0 || session.Snapshot.PITID == "" {
		return searchdomain.Session{}, sessionFailure(ctx, "create search session", session.ID, errors.New("session identity, snapshot, and tokens are required"))
	}
	record := sessionRecordFor(session)
	err = transactSession(ctx, s.db, s.clock, "create search session", session.ID, func(tr fdb.Transaction) error {
		var existing searchSessionRecord
		found, readErr := readSessionValue(tr, searchSessionKey(session.ID), &existing)
		if readErr != nil {
			return readErr
		}
		if found {
			return fmt.Errorf("session %s already exists", session.ID)
		}
		if versionErr := requireWrittenVersion(ctx, tr, session.AuthorityID, session.Query.Access.Version); versionErr != nil {
			return versionErr
		}
		if writeErr := writeSessionValue(tr, searchSessionKey(session.ID), record, maxSessionHeaderBytes); writeErr != nil {
			return writeErr
		}
		tokens := []byte(session.Snapshot.QueryTokens)
		for chunk := range record.TokenChunks {
			end := min(len(tokens), (chunk+1)*sessionTokenChunkBytes)
			tr.Set(fdb.Key(searchSessionTokenKey(session.ID, chunk)), tokens[chunk*sessionTokenChunkBytes:end])
		}
		tr.Set(fdb.Key(searchSessionExpiryKey(session.ID, session.IdleDeadline)), nil)
		tr.Set(fdb.Key(searchSessionPresenceKey(session.Query.Index, session.ID)), nil)
		tr.Set(fdb.Key(searchSessionVersionKey(session.AuthorityID, session.Query.Access.Version, session.AbsoluteDeadline, session.ID)), nil)
		return nil
	})
	if err != nil {
		return searchdomain.Session{}, err
	}
	telemetry.L(ctx).InfoContext(ctx, "search.session.created", slog.String("session_id", session.ID.String()),
		slog.String("index", session.Query.Index), slog.Int("token_bytes", record.TokenBytes))
	return session, nil
}

// Load reads the session header and reassembles its query tokens.
func (s *SearchSessionStore) Load(ctx context.Context, sessionID uuid.UUID) (loaded searchdomain.Session, err error) {
	defer telemetry.FDBOp(ctx, "store.search_session.load")(&err)
	var record searchSessionRecord
	var tokens bytes.Buffer
	err = transactSession(ctx, s.db, s.clock, "load search session", sessionID, func(tr fdb.Transaction) error {
		tokens.Reset()
		found, readErr := readSessionValue(tr, searchSessionKey(sessionID), &record)
		if readErr != nil {
			return readErr
		}
		if !found {
			return searchdomain.ErrSessionNotFound
		}
		keyRange, rangeErr := fdb.PrefixRange(searchSessionPartPrefix(sessionID, sessionTokenPart))
		if rangeErr != nil {
			return sessionStepError{operation: "create token range", err: rangeErr}
		}
		chunks, rangeErr := tr.GetRange(keyRange, fdb.RangeOptions{Limit: record.TokenChunks + 1}).GetSliceWithError()
		if rangeErr != nil {
			return sessionStepError{operation: "read query tokens", err: rangeErr}
		}
		if len(chunks) != record.TokenChunks {
			return fmt.Errorf("session has %d token chunks, want %d", len(chunks), record.TokenChunks)
		}
		for _, chunk := range chunks {
			tokens.Write(chunk.Value)
		}
		return nil
	})
	if err != nil {
		return searchdomain.Session{}, err
	}
	if tokens.Len() != record.TokenBytes {
		return searchdomain.Session{}, sessionFailure(ctx, "load search session", sessionID,
			fmt.Errorf("query tokens contain %d bytes, want %d", tokens.Len(), record.TokenBytes))
	}
	session, err := record.session(json.RawMessage(tokens.Bytes()))
	if err != nil {
		return searchdomain.Session{}, sessionFailure(ctx, "decode search session", sessionID, err)
	}
	return session, nil
}
