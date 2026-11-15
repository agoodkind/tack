package foundationdb

import (
	"context"
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
func NewSearchSessionStore(
	db fdb.Database,
	source clock.Clock,
	idleTimeout time.Duration,
) *SearchSessionStore {
	return &SearchSessionStore{db: db, clock: source, idleTimeout: idleTimeout}
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
