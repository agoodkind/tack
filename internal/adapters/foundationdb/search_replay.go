package foundationdb

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// maxSessionPositionBytes bounds the stored sort position and point-in-time
// ID.
const maxSessionPositionBytes = 8 << 10

// Replay returns the result IDs and completion state that the page at
// version committed.
//
// It does not advance or renew the session. The replay record does not store
// the consumed sort position or point-in-time ID.
//
//   - A saved replay remains readable after BeginCleanup marks its session
//     closing, until CleanupSlice deletes that replay record.
func (s *SearchSessionStore) Replay(ctx context.Context, sessionID uuid.UUID, version uint64) (commit searchdomain.PageCommit, found bool, err error) {
	defer telemetry.FDBOp(ctx, "store.search_session.replay")(&err)
	var record searchReplayRecord
	err = transactSession(ctx, s.db, s.clock, "read search replay", sessionID, func(tr fdb.Transaction) error {
		var readErr error
		found, readErr = readSessionValue(tr, searchSessionReplayKey(sessionID, version), &record)
		return readErr
	})
	if err != nil || !found {
		return searchdomain.PageCommit{}, false, err
	}
	return searchdomain.PageCommit{Sort: nil, PITID: "", Visited: nil, ResultIDs: record.ResultIDs, Complete: record.Complete}, true, nil
}

// HasVisited reports which node IDs the session already consumed.
//
// It reads the supplied IDs in one bounded transaction. It rejects a request
// with more than maxSessionVisitedIDs IDs.
//
//   - Repeated IDs still count toward the request size limit.
func (s *SearchSessionStore) HasVisited(ctx context.Context, sessionID uuid.UUID, nodeIDs []uuid.UUID) (visited map[uuid.UUID]bool, err error) {
	defer telemetry.FDBOp(ctx, "store.search_session.visited")(&err)
	if len(nodeIDs) > maxSessionVisitedIDs {
		return nil, sessionFailure(ctx, "read visited nodes", sessionID, fmt.Errorf("%d node IDs exceed the %d-ID bound", len(nodeIDs), maxSessionVisitedIDs))
	}
	visited = make(map[uuid.UUID]bool, len(nodeIDs))
	err = transactSession(ctx, s.db, s.clock, "read visited nodes", sessionID, func(tr fdb.Transaction) error {
		futures := make([]fdb.FutureByteSlice, len(nodeIDs))
		for position, nodeID := range nodeIDs {
			futures[position] = tr.Get(fdb.Key(searchSessionVisitedKey(sessionID, nodeID)))
		}
		for position, future := range futures {
			value, readErr := future.Get()
			if readErr != nil {
				return sessionStepError{operation: "read visited node " + nodeIDs[position].String(), err: readErr}
			}
			visited[nodeIDs[position]] = len(value) > 0
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return visited, nil
}

// CommitPage stores the consumed sort position, replacement point-in-time
// ID, visited IDs, replay record, completion, and every changed record's
// change events in one transaction.
//
// It renews only the idle deadline. A concurrent commit for the same version
// returns [searchdomain.ErrSessionChanged]. An uncertain commit also
// requires an incomplete session write event, even if a retry finds a
// changed session version.
//
//   - A successful page commit increments the session version.
//   - Failure to commit the incomplete event returns an error that matches
//     audit.ErrEventNotRecorded.
func (s *SearchSessionStore) CommitPage(ctx context.Context, sessionID uuid.UUID, version uint64, commit searchdomain.PageCommit) (committed searchdomain.Session, err error) {
	defer telemetry.FDBOp(ctx, "store.search_session.commit")(&err)
	if len(commit.Visited) > maxSessionVisitedIDs || len(commit.ResultIDs) > maxSessionResultIDs ||
		len(commit.Sort)+len(commit.PITID) > maxSessionPositionBytes || commit.PITID == "" {
		return searchdomain.Session{}, sessionFailure(ctx, "commit search page", sessionID, errors.New("the page exceeds its visited, result, or position bounds"))
	}
	changes, err := beginSessionAudit(ctx, "commit search page", sessionID)
	if err != nil {
		return searchdomain.Session{}, sessionRecordingFailure(ctx, sessionID, err)
	}
	var record searchSessionRecord
	var ownerID uuid.UUID
	err = transactSession(ctx, s.db, s.clock, "commit search page", sessionID, func(tr fdb.Transaction) error {
		changes.startAttempt()
		// Each attempt decodes into a new record. The sort value of an earlier
		// attempt must not replace an absent stored sort value.
		var stored searchSessionRecord
		found, readErr := readSessionValue(tr, searchSessionKey(sessionID), &stored)
		switch {
		case readErr != nil:
			return readErr
		case !found:
			return searchdomain.ErrSessionNotFound
		case stored.Version != version:
			return searchdomain.ErrSessionChanged
		case stored.Closing || !s.clock.Now().Before(stored.IdleDeadline) || !s.clock.Now().Before(stored.AbsoluteDeadline):
			return searchdomain.ErrSessionExpired
		}
		changes.bind(stored)
		ownerID = stored.PrincipalID
		var applyErr error
		record, applyErr = s.applyPageCommit(ctx, tr, changes, stored, version, commit)
		if applyErr != nil {
			return applyErr
		}
		if appendErr := changes.append(ctx, tr); appendErr != nil {
			return sessionRecordingFailure(ctx, sessionID, appendErr)
		}
		slog.DebugContext(ctx, "search.session.page_commit_ready", slog.String("session_id", sessionID.String()))
		return nil
	})
	if err != nil {
		return searchdomain.Session{}, s.recordWriteUncertain(ctx, changes, sessionID, ownerID, audit.VerbSearchSessionUpdate, err)
	}
	changes.finish(ctx)
	telemetry.L(ctx).DebugContext(ctx, "search.session.page_committed", slog.String("session_id", sessionID.String()),
		slog.Uint64("version", record.Version), slog.Int("results", len(commit.ResultIDs)), slog.Bool("complete", commit.Complete))
	return record.session(nil)
}

// applyPageCommit writes one page commit in tr, records its changes, and
// returns the new header. The caller filters commit.Visited at this session
// version. Each visited record in commit.Visited is new.
func (s *SearchSessionStore) applyPageCommit(ctx context.Context, tr fdb.Transaction, changes *sessionAudit, stored searchSessionRecord, version uint64, commit searchdomain.PageCommit) (searchSessionRecord, error) {
	sessionID := stored.ID
	for _, nodeID := range commit.Visited {
		tr.Set(fdb.Key(searchSessionVisitedKey(sessionID, nodeID)), []byte{1})
		if err := changes.recordVisitedCreate(sessionID, nodeID); err != nil {
			return stored, sessionRecordingFailure(ctx, sessionID, err)
		}
	}
	replay := searchReplayRecord{ResultIDs: commit.ResultIDs, Complete: commit.Complete}
	if err := writeSessionValue(tr, searchSessionReplayKey(sessionID, version), replay, maxSessionHeaderBytes); err != nil {
		return stored, err
	}
	if err := changes.recordReplayCreate(sessionID, version, replay); err != nil {
		return stored, sessionRecordingFailure(ctx, sessionID, err)
	}
	if err := clearPreviousReplay(ctx, tr, changes, sessionID, version); err != nil {
		return stored, err
	}
	record := stored
	tr.Clear(fdb.Key(searchSessionExpiryKey(sessionID, record.IdleDeadline)))
	record.IdleDeadline = s.clock.Now().Add(s.idleTimeout)
	if record.IdleDeadline.After(record.AbsoluteDeadline) {
		record.IdleDeadline = record.AbsoluteDeadline
	}
	tr.Set(fdb.Key(searchSessionExpiryKey(sessionID, record.IdleDeadline)), nil)
	if commit.Complete {
		clearSessionVersion(tr, record)
	}
	record.Sort, record.PITID, record.Complete, record.Version = commit.Sort, commit.PITID, commit.Complete, version+1
	if err := writeSessionValue(tr, searchSessionKey(sessionID), record, maxSessionHeaderBytes); err != nil {
		return stored, err
	}
	previous, current := sessionState{record: stored, tokens: nil}, sessionState{record: record, tokens: nil}
	return record, sessionRecordingFailure(ctx, sessionID, changes.recordSessionUpdate(previous, current))
}

// clearPreviousReplay reads the replay record of the previous version,
// clears it, and records its removal.
func clearPreviousReplay(ctx context.Context, tr fdb.Transaction, changes *sessionAudit, sessionID uuid.UUID, version uint64) error {
	if version == 0 {
		return nil
	}
	previousVersion := version - 1
	var previous searchReplayRecord
	found, err := readSessionValue(tr, searchSessionReplayKey(sessionID, previousVersion), &previous)
	if err != nil {
		return err
	}
	tr.Clear(fdb.Key(searchSessionReplayKey(sessionID, previousVersion)))
	if !found {
		return nil
	}
	return sessionRecordingFailure(ctx, sessionID, changes.recordReplayDelete(sessionID, previousVersion, previous))
}
