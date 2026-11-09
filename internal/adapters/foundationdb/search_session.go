package foundationdb

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
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
func NewSearchSessionStore(
	db fdb.Database,
	source clock.Clock,
	idleTimeout time.Duration,
) *SearchSessionStore {
	return &SearchSessionStore{db: db, clock: source, idleTimeout: idleTimeout}
}

// Create refuses a session when the authority no longer writes the session's access version.
// If the create transaction ends after a commit that may have applied, Create
// also commits an incomplete session write event. If that event cannot commit,
// the returned error matches audit.ErrEventNotRecorded.
func (s *SearchSessionStore) Create(
	ctx context.Context,
	session searchdomain.Session,
) (created searchdomain.Session, err error) {
	defer telemetry.FDBOp(ctx, "store.search_session.create")(&err)
	if session.ID == uuid.Nil || session.Version != 0 || len(session.Snapshot.QueryTokens) == 0 ||
		session.Snapshot.PITID == "" {
		return searchdomain.Session{}, sessionFailure(
			ctx,
			"create search session",
			session.ID,
			errors.New("session identity, snapshot, and tokens are required"),
		)
	}

	if len(session.Snapshot.QueryTokens) > maxSessionTokenBytes {
		return searchdomain.Session{}, sessionFailure(
			ctx,
			"create search session",
			session.ID,
			fmt.Errorf(
				"query tokens contain %d bytes, above the %d-byte bound: %w",
				len(session.Snapshot.QueryTokens),
				maxSessionTokenBytes,
				searchdomain.ErrInvalidQuery,
			),
		)
	}

	record := sessionRecordFor(session)
	changes, err := beginSessionAudit(ctx, "create search session", session.ID)
	if err != nil {
		return searchdomain.Session{}, sessionRecordingFailure(ctx, session.ID, err)
	}

	err = transactSession(
		ctx,
		s.db,
		s.clock,
		"create search session",
		session.ID,
		func(tr fdb.Transaction) error {
			changes.startAttempt()
			changes.bind(record)
			var existing searchSessionRecord
			found, readErr := readSessionValue(tr, searchSessionKey(session.ID), &existing)
			if readErr != nil {
				return readErr
			}

			if found {
				return fmt.Errorf("session %s already exists", session.ID)
			}

			if versionErr := requireWrittenVersion(
				ctx,
				tr,
				session.AuthorityID,
				session.Query.Access.Version,
			); versionErr != nil {
				return versionErr
			}

			if writeErr := writeSessionValue(
				tr,
				searchSessionKey(session.ID),
				record,
				maxSessionHeaderBytes,
			); writeErr != nil {
				return writeErr
			}

			writeSessionParts(tr, session, record)

			return appendSessionCreation(ctx, tr, session, record, changes)
		},
	)
	if err != nil {
		return searchdomain.Session{}, s.recordWriteUncertain(
			ctx,
			changes,
			session.ID,
			session.PrincipalID,
			audit.VerbSearchSessionCreate,
			err,
		)
	}

	changes.finish(ctx)
	telemetry.L(ctx).InfoContext(
		ctx,
		"search.session.created",
		slog.String("session_id", session.ID.String()),
		slog.String("index", session.Query.Index),
		slog.Int("token_bytes", record.TokenBytes),
	)

	return session, nil
}

// Load reads the session header and reassembles its query tokens. For a
// closing session, it returns the header without query tokens, because
// CleanupSlice deletes the token chunks before the header.
func (s *SearchSessionStore) Load(
	ctx context.Context,
	sessionID uuid.UUID,
) (loaded searchdomain.Session, err error) {
	defer telemetry.FDBOp(ctx, "store.search_session.load")(&err)
	var record searchSessionRecord
	var tokens []byte
	err = transactSession(
		ctx,
		s.db,
		s.clock,
		"load search session",
		sessionID,
		func(tr fdb.Transaction) error {
			tokens = nil
			found, readErr := readSessionValue(tr, searchSessionKey(sessionID), &record)
			if readErr != nil {
				return readErr
			}

			if !found {
				return searchdomain.ErrSessionNotFound
			}

			if record.Closing {
				return nil
			}

			keyRange, rangeErr := fdb.PrefixRange(
				searchSessionPartPrefix(sessionID, sessionTokenPart),
			)
			if rangeErr != nil {
				return sessionStepError{operation: "create token range", err: rangeErr}
			}

			chunks, rangeErr := tr.GetRange(
				keyRange,
				fdb.RangeOptions{Limit: record.TokenChunks + 1},
			).GetSliceWithError()
			if rangeErr != nil {
				return sessionStepError{operation: "read query tokens", err: rangeErr}
			}

			var assembleErr error
			tokens, assembleErr = assembleSessionTokens(record, chunks)

			return assembleErr
		},
	)
	if err != nil {
		return searchdomain.Session{}, err
	}

	session, err := record.session(tokens)
	if err != nil {
		return searchdomain.Session{}, sessionFailure(ctx, "decode search session", sessionID, err)
	}

	return session, nil
}
