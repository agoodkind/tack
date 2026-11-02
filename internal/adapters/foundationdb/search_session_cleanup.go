package foundationdb

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/telemetry"
)

// maxSessionCleanupKeys bounds the child records one cleanup slice deletes.
const maxSessionCleanupKeys = 100

// expirySessionIDPosition is the tuple position of the session ID in an
// expiry key: (family, minute, bucket, session).
const expirySessionIDPosition = 3

// BeginCleanup marks the session as closing, clears its version presence
// entry, and records the change in one transaction. A closing session does
// not accept a page commit. BeginCleanup returns nil for an absent or already
// closing session and does not record changes for those sessions.
func (s *SearchSessionStore) BeginCleanup(ctx context.Context, sessionID uuid.UUID) error {
	return s.beginCleanup(ctx, sessionID, nil)
}

func (s *SearchSessionStore) beginCleanup(ctx context.Context, sessionID uuid.UUID, fence fdb.Key) (err error) {
	defer telemetry.FDBOp(ctx, "store.search_session.begin_cleanup")(&err)
	changes, err := beginSessionAudit(ctx, "mark search session closing", sessionID)
	if err != nil {
		return err
	}
	err = transactSession(ctx, s.db, s.clock, "mark search session closing", sessionID, func(tr fdb.Transaction) error {
		changes.startAttempt()
		if fenceErr := requireRetireFence(tr, fence); fenceErr != nil {
			return fenceErr
		}
		var stored searchSessionRecord
		found, readErr := readSessionValue(tr, searchSessionKey(sessionID), &stored)
		if readErr != nil || !found || stored.Closing {
			return readErr
		}
		changes.bind(stored)
		clearSessionVersion(tr, stored)
		record := stored
		record.Closing = true
		if writeErr := writeSessionValue(tr, searchSessionKey(sessionID), record, maxSessionHeaderBytes); writeErr != nil {
			return writeErr
		}
		previous, current := sessionState{record: stored, tokens: nil}, sessionState{record: record, tokens: nil}
		if recordErr := changes.recordSessionUpdate(previous, current); recordErr != nil {
			return recordErr
		}
		if appendErr := changes.append(ctx, tr); appendErr != nil {
			return appendErr
		}
		slog.DebugContext(ctx, "search.session.close_commit_ready", slog.String("session_id", sessionID.String()))
		return nil
	})
	if err != nil {
		return err
	}
	changes.finish(ctx)
	return nil
}

// CleanupSlice deletes at most limit child records of a closing session and
// records each removed value in the same transaction. A slice that reads a
// query token chunk deletes every chunk. A slice that reads fewer than limit
// child records also deletes the header and the expiry, presence, and version
// presence keys. It reports whether the session is gone.
func (s *SearchSessionStore) CleanupSlice(ctx context.Context, sessionID uuid.UUID, limit int) (bool, error) {
	return s.cleanupSlice(ctx, sessionID, limit, nil)
}

func (s *SearchSessionStore) cleanupSlice(ctx context.Context, sessionID uuid.UUID, limit int, fence fdb.Key) (done bool, err error) {
	defer telemetry.FDBOp(ctx, "store.search_session.cleanup")(&err)
	if limit < 1 || limit > maxSessionCleanupKeys {
		return false, sessionFailure(ctx, "clean up search session", sessionID, fmt.Errorf("limit %d is outside 1 to %d", limit, maxSessionCleanupKeys))
	}
	changes, err := beginSessionAudit(ctx, "clean up search session", sessionID)
	if err != nil {
		return false, err
	}
	err = transactSession(ctx, s.db, s.clock, "clean up search session", sessionID, func(tr fdb.Transaction) error {
		done = false
		changes.startAttempt()
		if fenceErr := requireRetireFence(tr, fence); fenceErr != nil {
			return fenceErr
		}
		var record searchSessionRecord
		found, readErr := readSessionValue(tr, searchSessionKey(sessionID), &record)
		if readErr != nil {
			return readErr
		}
		if !found {
			done = true
			return nil
		}
		if !record.Closing {
			return errors.New("the session is not closing")
		}
		changes.bind(record)
		keyRange, rangeErr := fdb.PrefixRange(searchSessionChildPrefix(sessionID))
		if rangeErr != nil {
			return sessionStepError{operation: "create child range", err: rangeErr}
		}
		children, rangeErr := tr.GetRange(keyRange, fdb.RangeOptions{Limit: limit}).GetSliceWithError()
		if rangeErr != nil {
			return sessionStepError{operation: "read child records", err: rangeErr}
		}
		tokens, clearErr := changes.clearChildren(tr, sessionID, children)
		if clearErr != nil {
			return clearErr
		}
		state := sessionState{record: record, tokens: tokens}
		if len(children) == limit {
			if len(tokens) > 0 {
				if recordErr := changes.recordSessionUpdate(state, sessionState{record: record, tokens: nil}); recordErr != nil {
					return recordErr
				}
			}
			return changes.append(ctx, tr)
		}
		tr.Clear(fdb.Key(searchSessionExpiryKey(sessionID, record.IdleDeadline)))
		tr.Clear(fdb.Key(searchSessionPresenceKey(record.Index, sessionID)))
		clearSessionVersion(tr, record)
		tr.Clear(fdb.Key(searchSessionKey(sessionID)))
		done = true
		if recordErr := changes.recordSessionDelete(state); recordErr != nil {
			return recordErr
		}
		return changes.append(ctx, tr)
	})
	if err != nil {
		return false, err
	}
	changes.finish(ctx)
	if done {
		telemetry.L(ctx).InfoContext(ctx, "search.session.deleted", slog.String("session_id", sessionID.String()))
	}
	return done, nil
}

// ExpiredSessions returns at most limit session IDs. Each returned session
// has an expiry key in a minute bucket earlier than the current minute.
func (s *SearchSessionStore) ExpiredSessions(ctx context.Context, limit int) (sessionIDs []uuid.UUID, err error) {
	defer telemetry.FDBOp(ctx, "store.search_session.expired")(&err)
	begin, end := searchSessionExpiryRange(s.clock.Now())
	err = transactSession(ctx, s.db, s.clock, "list expired search sessions", uuid.Nil, func(tr fdb.Transaction) error {
		sessionIDs = sessionIDs[:0]
		keys, readErr := tr.GetRange(fdb.KeyRange{Begin: fdb.Key(begin), End: fdb.Key(end)}, fdb.RangeOptions{Limit: limit}).GetSliceWithError()
		if readErr != nil {
			return sessionStepError{operation: "read expiry keys", err: readErr}
		}
		for _, key := range keys {
			unpacked, unpackErr := tuple.Unpack(stripPrefix(key.Key))
			if unpackErr != nil || len(unpacked) <= expirySessionIDPosition {
				return fmt.Errorf("decode expiry key %x", key.Key)
			}
			text, _ := unpacked[expirySessionIDPosition].(string)
			sessionID, parseErr := uuid.Parse(text)
			if parseErr != nil {
				return sessionStepError{operation: "decode expired session ID " + text, err: parseErr}
			}
			sessionIDs = append(sessionIDs, sessionID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return sessionIDs, nil
}
