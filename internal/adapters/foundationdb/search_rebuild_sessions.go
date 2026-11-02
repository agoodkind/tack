package foundationdb

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// RetireIndexSessions recovers expired retirement attempt records for index,
// then deletes bounded slices of sessions that read index and cannot serve
// another page. It retains a continuable session until its idle or absolute
// deadline passes. It reports whether an active attempt, more attempt records,
// a continuable or partly deleted session, or a presence entry beyond the slice
// limit leaves work for another call. An active attempt can delay index
// deletion by up to retireAttemptLease.
func (s *SearchRebuildStore) RetireIndexSessions(ctx context.Context, index string, limit int) (remaining bool, err error) {
	defer telemetry.FDBOp(ctx, "store.search_rebuild.retire_sessions")(&err)
	attemptsRemain, err := s.recoverRetireAttempts(ctx, index, limit)
	if err != nil {
		return true, err
	}
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
	remaining = attemptsRemain || len(sessionIDs) == limit
	for _, sessionID := range sessionIDs {
		kept, retireErr := s.retireSession(ctx, index, sessionID, now)
		if retireErr != nil {
			return true, retireErr
		}
		remaining = remaining || kept
	}
	return remaining, nil
}

// retireSession deletes one bounded slice of a session that cannot serve
// another page. It directly removes a presence entry without a session
// header. It reports whether the session still exists afterward.
// When it cannot read or decode the session, it records a failed cleanup
// event through recordSessionLoadFailure and leaves the session and its
// presence entry in place.
func (s *SearchRebuildStore) retireSession(ctx context.Context, index string, sessionID uuid.UUID, now time.Time) (bool, error) {
	var record searchSessionRecord
	var found, headerAbsent bool
	var restoreEpoch int64
	err := transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		var readErr error
		headerAbsent = false
		found, readErr = readSessionValue(tr, searchSessionKey(sessionID), &record)
		if readErr != nil {
			return readErr
		}
		if !found {
			headerAbsent = true
			tr.Clear(fdb.Key(searchSessionPresenceKey(index, sessionID)))
			return nil
		}
		restoreEpoch, readErr = readSearchCounter(ctx, tr, searchRestoreEpochKey())
		return readErr
	})
	if err != nil {
		failure := searchStorageError(ctx, "search.rebuild.session_read_failed", "read session "+sessionID.String(), uuid.Nil, err)
		if headerAbsent {
			return true, failure
		}
		return true, s.recordSessionLoadFailure(ctx, sessionID, failure)
	}
	if !found {
		return false, nil
	}
	session, err := record.session(nil)
	if err != nil {
		failure := searchStorageError(ctx, "search.rebuild.session_read_failed", "decode session "+sessionID.String(), uuid.Nil, err)
		return true, s.recordSessionLoadFailure(ctx, sessionID, failure)
	}
	if session.Continuable(now, restoreEpoch) {
		return true, nil
	}
	if !record.Closing {
		err := s.retireStep(ctx, index, sessionID, record, searchdomain.CleanupClose, func(stepContext context.Context, fence fdb.Key) error {
			return s.sessions.beginCleanup(stepContext, sessionID, fence)
		})
		if err != nil {
			return true, err
		}
	}
	deleted := false
	err = s.retireStep(ctx, index, sessionID, record, searchdomain.CleanupDelete, func(stepContext context.Context, fence fdb.Key) error {
		var sliceErr error
		deleted, sliceErr = s.sessions.cleanupSlice(stepContext, sessionID, maxSessionCleanupKeys, fence)
		return sliceErr
	})
	if err != nil {
		return true, err
	}
	return !deleted, nil
}

// recordSessionLoadFailure calls RecordCleanupFailure with CleanupDelete,
// the step the search sweep records for the same failure. It returns cause
// when the event commits. When the event does not commit, it logs the
// recording error and returns an error that matches audit.ErrEventNotRecorded
// and contains cause and the recording error. Each call writes a new event,
// so each retirement slice that fails on a session adds one event.
func (s *SearchRebuildStore) recordSessionLoadFailure(ctx context.Context, sessionID uuid.UUID, cause error) error {
	err := s.sessions.RecordCleanupFailure(ctx, sessionID, searchdomain.CleanupDelete, cause)
	if err == nil {
		return cause
	}
	slog.ErrorContext(ctx, "search.session.audit_failed", slog.String("err", err.Error()), slog.String("session_id", sessionID.String()))
	return errors.Join(audit.ErrEventNotRecorded, cause,
		fmt.Errorf("record the failed cleanup step %s of search session %s: %w", searchdomain.CleanupDelete, sessionID, err))
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
