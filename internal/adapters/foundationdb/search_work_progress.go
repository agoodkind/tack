package foundationdb

import (
	"context"
	"strconv"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// CompletePage checkpoints one page after its OpenSearch write succeeded. The
// final page sets the checkpoint phase to PhaseRefresh.
func (s *SearchWorkStore) CompletePage(ctx context.Context, intent searchdomain.WriteIntent, cursor string, done bool) (err error) {
	defer telemetry.FDBOp(ctx, "store.search_work.complete_page")(&err)
	work := intent.Work
	page := intent.Page
	if intent.DocumentID != searchdomain.DocumentID(work.OrgID, work.NodeID, page.Revision, page.ProjectionVersion, page.Ordinal) {
		return searchStorageError(ctx, "search.work.document_changed", "validate completed document identity", work.NodeID, searchdomain.ErrWorkChanged)
	}
	revision, err := strconv.ParseInt(page.Revision, 10, 64)
	if err != nil {
		return searchStorageError(ctx, "search.work.revision_invalid", "parse completed page revision", work.NodeID, err)
	}
	phase := searchdomain.PhasePages
	if done {
		phase = searchdomain.PhaseRefresh
	}
	err = transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		if err := s.verifyPage(ctx, tr, work, page, revision); err != nil {
			return err
		}
		return writeSearchRecord(ctx, tr, searchCursorKey(string(work.Class), work.OrgID, work.NodeID), searchProgressRecord{
			Revision: revision, Generation: work.Generation, Projection: page.ProjectionVersion,
			Cursor: cursor, Ordinal: page.Ordinal + 1, Phase: string(phase),
		})
	})
	if err != nil {
		return searchStorageError(ctx, "search.work.checkpoint_failed", "checkpoint completed page", work.NodeID, err)
	}
	return nil
}

// CompleteRefresh finishes content work after the worker refreshes the
// claimed index, and schedules retirement of every older revision. A
// replacement copy finishes without scheduling retirement. Live work
// schedules cleanup for older revisions.
func (s *SearchWorkStore) CompleteRefresh(ctx context.Context, work searchdomain.Work) (err error) {
	defer telemetry.FDBOp(ctx, "store.search_work.complete_refresh")(&err)
	err = transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		record, err := s.verifyClaim(ctx, tr, work)
		if err != nil {
			return err
		}
		progress, err := s.readProgress(ctx, tr, work.Class, record)
		if err != nil {
			return err
		}
		if progress.Phase != string(searchdomain.PhaseRefresh) {
			return searchdomain.ErrWorkChanged
		}
		clearSearchWork(tr, work, record)
		if work.Class == searchdomain.WorkClassCopy {
			return nil
		}
		cleanup := record
		cleanup.Deleted = false
		cleanup.EnqueuedAt = s.clock.Now().UTC()
		pending, found, err := readSearchWork(ctx, tr, searchdomain.WorkClassCleanup, work.OrgID, work.NodeID)
		if err != nil {
			return err
		}
		var previous *searchWorkRecord
		if found {
			previous = &pending
		}
		return writeSearchWork(ctx, tr, searchdomain.WorkClassCleanup, cleanup, previous)
	})
	if err != nil {
		return searchStorageError(ctx, "search.work.refresh_checkpoint_failed", "complete refreshed search work", work.NodeID, err)
	}
	return nil
}

// Restart schedules a new content revision when the projected text changed
// under a continuation cursor.
func (s *SearchWorkStore) Restart(ctx context.Context, work searchdomain.Work) (err error) {
	defer telemetry.FDBOp(ctx, "store.search_work.restart")(&err)
	err = transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		if _, err := s.verifyClaim(ctx, tr, work); err != nil {
			return err
		}
		tr.Clear(fdb.Key(searchClaimKey(string(work.Class), searchBucket(work.OrgID, work.NodeID), work.OrgID, work.NodeID)))
		_, scheduleErr := scheduleSearchChange(ctx, tr, s.clock.Now(), work.OrgID, work.NodeID, searchChangeContent)
		return scheduleErr
	})
	if err != nil {
		return searchStorageError(ctx, "search.work.restart_failed", "restart changed search content", work.NodeID, err)
	}
	return nil
}

// Yield releases a claim while leaving its durable work and checkpoint pending.
func (s *SearchWorkStore) Yield(ctx context.Context, work searchdomain.Work) (err error) {
	defer telemetry.FDBOp(ctx, "store.search_work.yield")(&err)
	err = transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		if _, err := s.verifyClaim(ctx, tr, work); err != nil {
			return err
		}
		tr.Clear(fdb.Key(searchClaimKey(string(work.Class), searchBucket(work.OrgID, work.NodeID), work.OrgID, work.NodeID)))
		return nil
	})
	if err != nil {
		return searchStorageError(ctx, "search.work.yield_failed", "yield search work claim", work.NodeID, err)
	}
	return nil
}

// Release records one failure and delays the next claim of the same work.
func (s *SearchWorkStore) Release(ctx context.Context, work searchdomain.Work, message string) (err error) {
	defer telemetry.FDBOp(ctx, "store.search_work.release")(&err)
	err = transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		if _, err := s.verifyClaim(ctx, tr, work); err != nil {
			return err
		}
		tr.Set(fdb.Key(searchErrorKey(string(work.Class), work.OrgID, work.NodeID)), []byte(message))
		return writeSearchRecord(ctx, tr, searchClaimKey(string(work.Class), searchBucket(work.OrgID, work.NodeID), work.OrgID, work.NodeID), searchClaimRecord{
			Owner: "", Generation: work.Generation, LeaseUntil: s.clock.Now().Add(searchRetryDelay), Target: work.Target, Mirror: work.Mirror,
		})
	})
	if err != nil {
		return searchStorageError(ctx, "search.work.release_failed", "release failed search work", work.NodeID, err)
	}
	return nil
}

// clearSearchWork removes the finished record, its class-age entry, claim,
// checkpoint, and error of work's class. record is the pending record the
// claim verification read.
func clearSearchWork(tr fdb.Transaction, work searchdomain.Work, record searchWorkRecord) {
	class := string(work.Class)
	bucket := searchBucket(work.OrgID, work.NodeID)
	removeSearchWork(tr, work.Class, record)
	tr.Clear(fdb.Key(searchClaimKey(class, bucket, work.OrgID, work.NodeID)))
	tr.Clear(fdb.Key(searchCursorKey(class, work.OrgID, work.NodeID)))
	tr.Clear(fdb.Key(searchErrorKey(class, work.OrgID, work.NodeID)))
}
