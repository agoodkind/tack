package foundationdb

import (
	"context"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// AdvanceRebuild replaces the replacement state the worker read with next.
// The transaction verifies the rebuild claim and requires the stored state
// to equal read. A worker that read an older step writes nothing.
func (s *SearchRebuildStore) AdvanceRebuild(ctx context.Context, work searchdomain.Work, read, next searchdomain.Rebuild) (err error) {
	defer telemetry.FDBOp(ctx, "store.search_rebuild.advance")(&err)
	err = s.rebuildStep(ctx, work, read, func(tr fdb.Transaction, _ searchWorkRecord) error {
		return writeSearchRecord(ctx, tr, searchRebuildKey(), rebuildRecordFor(next))
	})
	if err != nil {
		return searchStorageError(ctx, "search.rebuild.advance_failed", "advance index replacement "+read.ID.String(), uuid.Nil, err)
	}
	telemetry.L(ctx).InfoContext(ctx, "search.rebuild.advanced", slog.String("rebuild_id", read.ID.String()),
		slog.String("state", string(next.State)), slog.Bool("paused", next.Paused))
	return nil
}

// CompleteSwitch records the target as the serving index after the public
// alias selects it, resumes claims, and starts retiring the source. After
// this transaction commits, new sessions and new claims use the new index.
func (s *SearchRebuildStore) CompleteSwitch(ctx context.Context, work searchdomain.Work, read searchdomain.Rebuild) (err error) {
	defer telemetry.FDBOp(ctx, "store.search_rebuild.complete_switch")(&err)
	err = s.rebuildStep(ctx, work, read, func(tr fdb.Transaction, _ searchWorkRecord) error {
		if read.State != searchdomain.RebuildSwitching {
			return searchdomain.ErrWorkChanged
		}
		tr.Set(fdb.Key(searchIndexKey()), []byte(read.TargetIndex))
		next := read
		next.State, next.Paused, next.VerifyCursor = searchdomain.RebuildRetiring, false, ""
		return writeSearchRecord(ctx, tr, searchRebuildKey(), rebuildRecordFor(next))
	})
	if err != nil {
		return searchStorageError(ctx, "search.rebuild.switch_failed", "record switched index "+read.TargetIndex, uuid.Nil, err)
	}
	telemetry.L(ctx).InfoContext(ctx, "search.rebuild.switched", slog.String("rebuild_id", read.ID.String()),
		slog.String("index", read.TargetIndex), slog.String("retiring_index", read.SourceIndex))
	return nil
}

// FinishRebuild ends the replacement after the retiring or failed index was
// deleted. It clears the replacement record, the retirement time of the
// deleted index, and the rebuild work item. Another replacement may begin.
func (s *SearchRebuildStore) FinishRebuild(ctx context.Context, work searchdomain.Work, read searchdomain.Rebuild) (err error) {
	defer telemetry.FDBOp(ctx, "store.search_rebuild.finish")(&err)
	deleted := read.SourceIndex
	if read.State == searchdomain.RebuildFailed {
		deleted = read.TargetIndex
	}
	err = s.rebuildStep(ctx, work, read, func(tr fdb.Transaction, record searchWorkRecord) error {
		tr.Clear(fdb.Key(searchRebuildKey()))
		tr.Clear(fdb.Key(searchRetiredSinceKey(deleted)))
		clearSearchWork(tr, work, record)
		return nil
	})
	if err != nil {
		return searchStorageError(ctx, "search.rebuild.finish_failed", "finish index replacement "+read.ID.String(), uuid.Nil, err)
	}
	telemetry.L(ctx).InfoContext(ctx, "search.rebuild.finished", slog.String("rebuild_id", read.ID.String()),
		slog.String("state", string(read.State)), slog.String("deleted_index", deleted), slog.String("failure", read.Failure))
	return nil
}

// rebuildStep verifies the rebuild claim and the stored state the worker
// read, then applies step in the same transaction. step receives the
// verified rebuild work record.
func (s *SearchRebuildStore) rebuildStep(ctx context.Context, work searchdomain.Work, read searchdomain.Rebuild, step func(fdb.Transaction, searchWorkRecord) error) error {
	return transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		record, err := s.work.verifyClaim(ctx, tr, work)
		if err != nil {
			return err
		}
		stored, found, err := readRebuild(ctx, tr)
		if err != nil {
			return err
		}
		if !found || !sameRebuild(stored, read) {
			return searchdomain.ErrWorkChanged
		}
		return step(tr, record)
	})
}

// sameRebuild reports whether two reads observed the same replacement step.
func sameRebuild(left, right searchdomain.Rebuild) bool {
	return left.ID == right.ID && left.State == right.State && left.Generation == right.Generation &&
		left.ScanCursor == right.ScanCursor && left.VerifyCursor == right.VerifyCursor &&
		left.ScanComplete == right.ScanComplete && left.Paused == right.Paused && left.PausedAt.Equal(right.PausedAt)
}
