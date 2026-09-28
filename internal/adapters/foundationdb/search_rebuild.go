package foundationdb

import (
	"context"
	"errors"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// replacementIndexPrefix starts the name of every replacement index. The
// name suffix is the replacement's UUIDv7.
const replacementIndexPrefix = "node-pages-"

// SearchRebuildStore persists the one index replacement in FoundationDB.
// Each step runs under a claimed rebuild work item.
type SearchRebuildStore struct {
	db       fdb.Database
	work     *SearchWorkStore
	sessions *SearchSessionStore
}

var _ searchdomain.RebuildStore = (*SearchRebuildStore)(nil)

// NewSearchRebuildStore creates the replacement store. work verifies claims
// and supplies the injected clock. sessions deletes the sessions of a
// retiring index.
func NewSearchRebuildStore(db fdb.Database, work *SearchWorkStore, sessions *SearchSessionStore) *SearchRebuildStore {
	return &SearchRebuildStore{db: db, work: work, sessions: sessions}
}

// CurrentRebuild returns the one replacement and whether it exists.
func (s *SearchRebuildStore) CurrentRebuild(ctx context.Context) (rebuild searchdomain.Rebuild, found bool, err error) {
	defer telemetry.FDBOp(ctx, "store.search_rebuild.current")(&err)
	err = transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		var readErr error
		rebuild, found, readErr = readRebuild(ctx, tr)
		return readErr
	})
	if err != nil {
		return rebuild, false, searchStorageError(ctx, "search.rebuild.read_failed", "read index replacement", uuid.Nil, err)
	}
	return rebuild, found, nil
}

// BeginRebuild records one replacement of the serving index and schedules
// its rebuild work item in the same transaction. The stored record refuses
// every other replacement until the old index is retired. A restored
// replacement also increments the restore epoch, which every session binds.
func (s *SearchRebuildStore) BeginRebuild(ctx context.Context, request searchdomain.BeginRebuild) (rebuild searchdomain.Rebuild, err error) {
	defer telemetry.FDBOp(ctx, "store.search_rebuild.begin")(&err)
	if err := request.Validate(); err != nil {
		return rebuild, searchStorageError(ctx, "search.rebuild.request_invalid", "validate index replacement", uuid.Nil, err)
	}
	identifier := uuid.Must(uuid.NewV7())
	err = transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		if _, found, readErr := readRebuild(ctx, tr); readErr != nil || found {
			return rebuildConflict(readErr, found)
		}
		serving, readErr := tr.Get(fdb.Key(searchIndexKey())).Get()
		if readErr != nil {
			return searchReadFailure(ctx, "read serving search index", readErr)
		}
		if len(serving) == 0 {
			return searchdomain.ErrNoServingIndex
		}
		generation, counterErr := incrementSearchCounter(ctx, tr, searchRebuildGenerationKey())
		if counterErr != nil {
			return counterErr
		}
		if request.Restored {
			if _, epochErr := incrementSearchCounter(ctx, tr, searchRestoreEpochKey()); epochErr != nil {
				return epochErr
			}
		}
		rebuild = searchdomain.Rebuild{
			ID: identifier, Mode: request.Mode, State: searchdomain.RebuildCreating,
			SourceIndex: string(serving), TargetIndex: replacementIndexPrefix + identifier.String(),
			ScanCursor: "", VerifyCursor: "", Failure: "", PrimaryShards: request.PrimaryShards,
			RoutingShards: request.RoutingShards, Replicas: request.Replicas, ScanComplete: false,
			Paused: false, Restored: request.Restored, PausedAt: s.work.clock.Now().UTC(), Generation: generation,
		}
		if writeErr := writeSearchRecord(ctx, tr, searchRebuildKey(), rebuildRecordFor(rebuild)); writeErr != nil {
			return writeErr
		}
		return writeSearchWork(ctx, tr, searchdomain.WorkClassRebuild, searchWorkRecord{
			OrgID: uuid.Nil, NodeID: uuid.Nil, Generation: generation, Revision: 0,
			Deleted: false, EnqueuedAt: s.work.clock.Now().UTC(),
		}, nil)
	})
	if errors.Is(err, searchdomain.ErrRebuildInProgress) || errors.Is(err, searchdomain.ErrNoServingIndex) {
		return rebuild, err
	}
	if err != nil {
		return rebuild, searchStorageError(ctx, "search.rebuild.begin_failed", "begin index replacement", uuid.Nil, err)
	}
	telemetry.L(ctx).InfoContext(ctx, "search.rebuild.begun", slog.String("rebuild_id", rebuild.ID.String()),
		slog.String("mode", string(rebuild.Mode)), slog.String("index", rebuild.SourceIndex),
		slog.String("target_index", rebuild.TargetIndex), slog.Bool("restored", rebuild.Restored),
		slog.String("reason", request.Reason))
	return rebuild, nil
}

func rebuildConflict(err error, found bool) error {
	if err != nil {
		return err
	}
	if found {
		return searchdomain.ErrRebuildInProgress
	}
	return nil
}

// WaitRebuild releases the rebuild claim for the retry delay without
// recording a failure. The replacement waits for leases, copies, pending
// page work, or sessions.
func (s *SearchRebuildStore) WaitRebuild(ctx context.Context, work searchdomain.Work) (err error) {
	defer telemetry.FDBOp(ctx, "store.search_rebuild.wait")(&err)
	err = transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		if _, verifyErr := s.work.verifyClaim(ctx, tr, work); verifyErr != nil {
			return verifyErr
		}
		return writeSearchRecord(ctx, tr, searchClaimKey(string(work.Class), searchBucket(work.OrgID, work.NodeID), work.OrgID, work.NodeID), searchClaimRecord{
			Owner: "", Generation: work.Generation, LeaseUntil: s.work.clock.Now().Add(searchRetryDelay), Target: work.Target, Mirror: work.Mirror,
		})
	})
	if err != nil {
		return searchStorageError(ctx, "search.rebuild.wait_failed", "delay index replacement", uuid.Nil, err)
	}
	return nil
}
