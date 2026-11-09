package foundationdb

import (
	"context"
	"errors"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
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
	recorder audit.Recorder
}

var _ searchdomain.RebuildStore = (*SearchRebuildStore)(nil)

// NewSearchRebuildStore creates the index replacement store.
//
// work verifies claims and supplies the injected clock. sessions deletes
// sessions of a retiring index.
//
//   - The constructor leaves the external failure recorder nil.
func NewSearchRebuildStore(db fdb.Database, work *SearchWorkStore, sessions *SearchSessionStore) *SearchRebuildStore {
	return &SearchRebuildStore{db: db, work: work, sessions: sessions, recorder: nil}
}

// WithFailureRecorder returns a copy of the store with recorder for
// retirement failure evidence.
//
// The store uses recorder when FoundationDB cannot save the failed event and
// its failure codes. A nil recorder disables this recording path.
//
//   - The recording call ignores caller cancellation and uses
//     audit.ExternalResultDeadline.
//   - The failure event includes both cleanup and recording failure codes.
func (s *SearchRebuildStore) WithFailureRecorder(recorder audit.Recorder) *SearchRebuildStore {
	withRecorder := *s
	withRecorder.recorder = recorder
	return &withRecorder
}

// CurrentRebuild returns the stored index replacement and whether it exists.
//
// The function reads the environment's single replacement record in one
// transaction.
//
//   - An absent record returns a zero Rebuild value, false, and nil.
//   - A failed read returns found as false with a storage error.
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
// rebuild work in the same transaction.
//
// The stored replacement refuses another replacement until completion
// removes its record after retirement or failure cleanup. A restored
// replacement increments the restore epoch that every session records.
//
//   - The replacement starts in RebuildCreating with a new UUIDv7 and a
//     separate rebuild generation.
//   - The record preserves request.Initiation for later operation and actor
//     evidence.
//   - A missing serving index returns [searchdomain.ErrNoServingIndex].
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
			Initiation: request.Initiation,
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
// recording a failure.
//
// The replacement waits for leases, copies, pending page work, or sessions.
//
//   - The released claim retains its generation, target, and mirror.
//   - The next eligible claim waits until the new LeaseUntil.
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
