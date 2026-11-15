package foundationdb

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// maxSessionPositionBytes bounds the stored sort position and point-in-time ID.
const maxSessionPositionBytes = 8 << 10

// Replay returns the result IDs and completion state that the page at
// version committed. It neither advances nor renews the session.
func (s *SearchSessionStore) Replay(
	ctx context.Context,
	sessionID uuid.UUID,
	version uint64,
) (commit searchdomain.PageCommit, found bool, err error) {
	defer telemetry.FDBOp(ctx, "store.search_session.replay")(&err)
	var record searchReplayRecord
	err = transactSession(
		ctx,
		s.db,
		s.clock,
		"read search replay",
		sessionID,
		func(tr fdb.Transaction) error {
			var readErr error
			found, readErr = readSessionValue(
				tr,
				searchSessionReplayKey(sessionID, version),
				&record,
			)

			return readErr
		},
	)
	if err != nil || !found {
		return searchdomain.PageCommit{}, false, err
	}

	return searchdomain.PageCommit{
		Sort:      nil,
		PITID:     "",
		Visited:   nil,
		ResultIDs: record.ResultIDs,
		Complete:  record.Complete,
	}, true, nil
}

// HasVisited reports which node IDs the session already consumed, in one
// bounded read transaction.
func (s *SearchSessionStore) HasVisited(
	ctx context.Context,
	sessionID uuid.UUID,
	nodeIDs []uuid.UUID,
) (visited map[uuid.UUID]bool, err error) {
	defer telemetry.FDBOp(ctx, "store.search_session.visited")(&err)
	if len(nodeIDs) > maxSessionVisitedIDs {
		return nil, sessionFailure(
			ctx,
			"read visited nodes",
			sessionID,
			fmt.Errorf("%d node IDs exceed the %d-ID bound", len(nodeIDs), maxSessionVisitedIDs),
		)
	}

	visited = make(map[uuid.UUID]bool, len(nodeIDs))
	err = transactSession(
		ctx,
		s.db,
		s.clock,
		"read visited nodes",
		sessionID,
		func(tr fdb.Transaction) error {
			futures := make([]fdb.FutureByteSlice, len(nodeIDs))
			for position, nodeID := range nodeIDs {
				futures[position] = tr.Get(fdb.Key(searchSessionVisitedKey(sessionID, nodeID)))
			}

			for position, future := range futures {
				value, readErr := future.Get()
				if readErr != nil {
					return sessionStepError{
						operation: "read visited node " + nodeIDs[position].String(),
						err:       readErr,
					}
				}

				visited[nodeIDs[position]] = len(value) > 0
			}

			return nil
		},
	)
	if err != nil {
		return nil, err
	}

	return visited, nil
}

// CommitPage atomically stores page progress, visited IDs, and replay results.
// A concurrent commit for this version returns searchdomain.ErrSessionChanged.
func (s *SearchSessionStore) CommitPage(
	ctx context.Context,
	sessionID uuid.UUID,
	version uint64,
	commit searchdomain.PageCommit,
) (committed searchdomain.Session, err error) {
	defer telemetry.FDBOp(ctx, "store.search_session.commit")(&err)
	if len(commit.Visited) > maxSessionVisitedIDs || len(commit.ResultIDs) > maxSessionResultIDs ||
		len(commit.Sort)+len(commit.PITID) > maxSessionPositionBytes || commit.PITID == "" {
		return searchdomain.Session{}, sessionFailure(
			ctx,
			"commit search page",
			sessionID,
			errors.New("the page exceeds its visited, result, or position bounds"),
		)
	}

	var record searchSessionRecord
	err = transactSession(
		ctx,
		s.db,
		s.clock,
		"commit search page",
		sessionID,
		func(tr fdb.Transaction) error {
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
			case stored.Closing || !s.clock.Now().Before(
				stored.IdleDeadline,
			) || !s.clock.Now().Before(stored.AbsoluteDeadline):
				return searchdomain.ErrSessionExpired
			}

			var applyErr error
			record, applyErr = s.applyPageCommit(tr, stored, version, commit)
			if applyErr != nil {
				return applyErr
			}

			slog.DebugContext(
				ctx,
				"search.session.page_commit_ready",
				slog.String("session_id", sessionID.String()),
			)

			return nil
		},
	)
	if err != nil {
		return searchdomain.Session{}, err
	}

	telemetry.L(ctx).DebugContext(
		ctx,
		"search.session.page_committed",
		slog.String("session_id", sessionID.String()),
		slog.Uint64("version", record.Version),
		slog.Int("results", len(commit.ResultIDs)),
		slog.Bool("complete", commit.Complete),
	)

	return record.session(nil)
}

// applyPageCommit writes one page commit in tr and returns the new header.
// The caller filters commit.Visited at this session
// version. Each visited record in commit.Visited is new.
func (s *SearchSessionStore) applyPageCommit(
	tr fdb.Transaction,
	stored searchSessionRecord,
	version uint64,
	commit searchdomain.PageCommit,
) (searchSessionRecord, error) {
	sessionID := stored.ID

	for _, nodeID := range commit.Visited {
		tr.Set(fdb.Key(searchSessionVisitedKey(sessionID, nodeID)), []byte{1})
	}

	replay := searchReplayRecord{ResultIDs: commit.ResultIDs, Complete: commit.Complete}
	if err := writeSessionValue(
		tr,
		searchSessionReplayKey(sessionID, version),
		replay,
		maxSessionHeaderBytes,
	); err != nil {
		return stored, err
	}

	if version > 0 {
		tr.Clear(fdb.Key(searchSessionReplayKey(sessionID, version-1)))
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
	if err := writeSessionValue(
		tr,
		searchSessionKey(sessionID),
		record,
		maxSessionHeaderBytes,
	); err != nil {
		return stored, err
	}

	return record, nil
}
