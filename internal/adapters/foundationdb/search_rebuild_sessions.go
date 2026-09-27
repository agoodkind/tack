package foundationdb

import (
	"context"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/telemetry"
)

// RetireIndexSessions deletes, in bounded slices, the sessions that read
// index and can serve no further page. A continuable session stays until
// its idle or absolute deadline passes. It reports whether a session of index
// remains after this slice: a continuable session, a partly deleted session,
// or a presence entry beyond the slice limit.
func (s *SearchRebuildStore) RetireIndexSessions(ctx context.Context, index string, limit int) (remaining bool, err error) {
	defer telemetry.FDBOp(ctx, "store.search_rebuild.retire_sessions")(&err)
	var sessionIDs []uuid.UUID
	err = transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		keyRange, rangeErr := fdb.PrefixRange(searchSessionPresencePrefix(index))
		if rangeErr != nil {
			return searchReadFailure(ctx, "create session presence range", rangeErr)
		}
		items, readErr := tr.GetRange(keyRange, fdb.RangeOptions{Limit: limit}).GetSliceWithError()
		if readErr != nil {
			return searchReadFailure(ctx, "read session presence", readErr)
		}
		sessionIDs = make([]uuid.UUID, 0, len(items))
		for _, item := range items {
			sessionID, decodeErr := lastTupleID(ctx, item.Key)
			if decodeErr != nil {
				return decodeErr
			}
			sessionIDs = append(sessionIDs, sessionID)
		}
		return nil
	})
	if err != nil {
		return false, searchStorageError(ctx, "search.rebuild.sessions_failed", "read sessions of index "+index, uuid.Nil, err)
	}
	now := s.work.clock.Now()
	remaining = len(sessionIDs) == limit
	for _, sessionID := range sessionIDs {
		kept, retireErr := s.retireSession(ctx, index, sessionID, now)
		if retireErr != nil {
			return true, retireErr
		}
		remaining = remaining || kept
	}
	return remaining, nil
}

// retireSession deletes one bounded slice of a session that can serve no
// further page. A presence entry without a session header is removed
// directly. It reports whether the session still exists afterward.
func (s *SearchRebuildStore) retireSession(ctx context.Context, index string, sessionID uuid.UUID, now time.Time) (bool, error) {
	var record searchSessionRecord
	var found bool
	var restoreEpoch int64
	err := transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		var readErr error
		found, readErr = readSessionValue(tr, searchSessionKey(sessionID), &record)
		if readErr != nil {
			return readErr
		}
		if !found {
			tr.Clear(fdb.Key(searchSessionPresenceKey(index, sessionID)))
			return nil
		}
		restoreEpoch, readErr = readSearchCounter(ctx, tr, searchRestoreEpochKey())
		return readErr
	})
	if err != nil {
		return true, searchStorageError(ctx, "search.rebuild.session_read_failed", "read session "+sessionID.String(), uuid.Nil, err)
	}
	if !found {
		return false, nil
	}
	session, err := record.session(nil)
	if err != nil {
		return true, searchStorageError(ctx, "search.rebuild.session_read_failed", "decode session "+sessionID.String(), uuid.Nil, err)
	}
	if session.Continuable(now, restoreEpoch) {
		return true, nil
	}
	if err := s.sessions.BeginCleanup(ctx, sessionID); err != nil {
		return true, err
	}
	deleted, err := s.sessions.CleanupSlice(ctx, sessionID, maxSessionCleanupKeys)
	return !deleted, err
}

// RetiredSince returns when the first page of index was retired.
func (s *SearchRebuildStore) RetiredSince(ctx context.Context, index string) (since time.Time, found bool, err error) {
	defer telemetry.FDBOp(ctx, "store.search_rebuild.retired_since")(&err)
	var encoded []byte
	err = transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		var readErr error
		encoded, readErr = tr.Get(fdb.Key(searchRetiredSinceKey(index))).Get()
		if readErr != nil {
			return searchReadFailure(ctx, "read retirement time of index "+index, readErr)
		}
		return nil
	})
	if err != nil {
		return since, false, searchStorageError(ctx, "search.rebuild.retired_since_failed", "read retirement time of index "+index, uuid.Nil, err)
	}
	if len(encoded) == 0 {
		return since, false, nil
	}
	nanos, err := unpackCounter(ctx, encoded)
	if err != nil {
		return since, false, err
	}
	return time.Unix(0, nanos).UTC(), true, nil
}

// recordRetiredSince stores now as the first retirement time of index when
// no earlier time exists.
func recordRetiredSince(ctx context.Context, tr fdb.Transaction, index string, now time.Time) error {
	if index == "" {
		return nil
	}
	existing, err := tr.Get(fdb.Key(searchRetiredSinceKey(index))).Get()
	if err != nil {
		return searchReadFailure(ctx, "read retirement time of index "+index, err)
	}
	if len(existing) == 0 {
		tr.Set(fdb.Key(searchRetiredSinceKey(index)), tuple.Tuple{now.UnixNano()}.Pack())
	}
	return nil
}
