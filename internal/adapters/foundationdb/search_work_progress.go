package foundationdb

import (
	"context"
	"strconv"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// CompletePage commits the page checkpoint and any owed replacement copy in
// one FoundationDB transaction.
//
// A mirror failure does not prevent progress after the target index accepts
// the page. The transaction schedules the owed copy before saving the
// checkpoint.
//
//   - A final page changes the work phase to refresh.
//   - Another page keeps the pages phase and increments the next ordinal.
func (s *SearchWorkStore) CompletePage(
	ctx context.Context,
	intent searchdomain.WriteIntent,
	cursor string,
	done bool,
) (err error) {
	defer telemetry.FDBOp(ctx, "store.search_work.complete_page")(&err)
	work := intent.Work
	page := intent.Page
	if intent.DocumentID != searchdomain.DocumentID(
		work.OrgID,
		work.NodeID,
		page.Revision,
		page.ProjectionVersion,
		page.Ordinal,
	) {
		return searchStorageError(
			ctx,
			"search.work.document_changed",
			"validate completed document identity",
			work.NodeID,
			searchdomain.ErrWorkChanged,
		)
	}

	revision, err := strconv.ParseInt(page.Revision, 10, 64)
	if err != nil {
		return searchStorageError(
			ctx,
			"search.work.revision_invalid",
			"parse completed page revision",
			work.NodeID,
			err,
		)
	}

	phase := searchdomain.PhasePages
	if done {
		phase = searchdomain.PhaseRefresh
	}

	err = transactSearch(
		ctx,
		s.db,
		func(tr fdb.Transaction) error {
			if _, err := s.verifyPage(ctx, tr, work, page, revision); err != nil {
				return err
			}

			if intent.Pending != nil {
				clearPendingWrite(
					tr,
					work.Class,
					work.OrgID,
					work.NodeID,
					intent.Pending.DocumentID,
					intent.Pending.Index,
				)
			}

			if intent.MirrorMissed != nil {
				if err := scheduleMirrorCopy(
					ctx,
					tr,
					s.clock.Now().UTC(),
					intent,
					revision,
				); err != nil {
					return err
				}
			}

			return writeSearchRecord(
				ctx,
				tr,
				searchCursorKey(string(work.Class), work.OrgID, work.NodeID),
				searchProgressRecord{
					Revision:   revision,
					Generation: work.Generation,
					Projection: page.ProjectionVersion,
					Cursor:     cursor,
					Ordinal:    page.Ordinal + 1,
					Phase:      string(phase),
				},
			)
		},
	)
	if err != nil {
		return searchStorageError(
			ctx,
			"search.work.checkpoint_failed",
			"checkpoint completed page",
			work.NodeID,
			err,
		)
	}

	return nil
}

// CompleteRefresh finishes content work after the worker refreshes the
// claimed index.
//
// It clears the node's exclusion. Live work schedules cleanup to retire
// every older revision. A replacement copy finishes without scheduling
// retirement.
//
//   - Work outside the refresh phase returns [searchdomain.ErrWorkChanged].
//   - An obsolete projection restarts work before completion.
func (s *SearchWorkStore) CompleteRefresh(ctx context.Context, work searchdomain.Work) (err error) {
	defer telemetry.FDBOp(ctx, "store.search_work.complete_refresh")(&err)
	err = transactSearch(
		ctx,
		s.db,
		func(tr fdb.Transaction) error {
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

			restarted, err := restartObsoleteProjection(
				ctx,
				tr,
				s.clock.Now(),
				work,
				progress.Projection,
			)
			if err != nil || restarted {
				return err
			}

			if err := promotePropertyWarnings(
				ctx,
				tr,
				work,
				record.Revision,
				progress.Projection,
			); err != nil {
				return err
			}

			clearSearchWork(tr, work, record)
			tr.Clear(fdb.Key(searchExclusionKey(work.OrgID, work.NodeID)))
			if work.Class == searchdomain.WorkClassCopy {
				return nil
			}

			cleanup := record
			cleanup.Deleted = false
			cleanup.EnqueuedAt = s.clock.Now().UTC()
			pending, found, err := readSearchWork(
				ctx,
				tr,
				searchdomain.WorkClassCleanup,
				work.OrgID,
				work.NodeID,
			)
			if err != nil {
				return err
			}

			var previous *searchWorkRecord
			if found {
				previous = &pending
			}

			return writeSearchWork(ctx, tr, searchdomain.WorkClassCleanup, cleanup, previous)
		},
	)
	if err != nil {
		return searchStorageError(
			ctx,
			"search.work.refresh_checkpoint_failed",
			"complete refreshed search work",
			work.NodeID,
			err,
		)
	}

	return nil
}

// Restart schedules a new content revision when the projected text changes
// under a continuation cursor.
//
// The transaction verifies the current claim and clears it before scheduling
// the content change. The new revision replaces work tied to the previous
// text.
//
//   - Content scheduling increments the node generation.
//   - The new live work begins without the previous content checkpoint.
func (s *SearchWorkStore) Restart(ctx context.Context, work searchdomain.Work) (err error) {
	defer telemetry.FDBOp(ctx, "store.search_work.restart")(&err)
	err = transactSearch(
		ctx,
		s.db,
		func(tr fdb.Transaction) error {
			if _, err := s.verifyClaim(ctx, tr, work); err != nil {
				return err
			}

			tr.Clear(
				fdb.Key(
					searchClaimKey(
						string(work.Class),
						searchBucket(work.OrgID, work.NodeID),
						work.OrgID,
						work.NodeID,
					),
				),
			)
			_, scheduleErr := scheduleSearchChange(
				ctx,
				tr,
				s.clock.Now(),
				work.OrgID,
				work.NodeID,
				searchChangeContent,
			)

			return scheduleErr
		},
	)
	if err != nil {
		return searchStorageError(
			ctx,
			"search.work.restart_failed",
			"restart changed search content",
			work.NodeID,
			err,
		)
	}

	return nil
}

// Yield releases a claim while leaving its durable work and checkpoint
// pending.
//
// The transaction verifies the claim before clearing its key. Yield does not
// record a failure or delay the next claim.
//
//   - A later owner can resume from the saved checkpoint.
//   - A changed claim returns [searchdomain.ErrWorkChanged].
func (s *SearchWorkStore) Yield(ctx context.Context, work searchdomain.Work) (err error) {
	defer telemetry.FDBOp(ctx, "store.search_work.yield")(&err)
	err = transactSearch(
		ctx,
		s.db,
		func(tr fdb.Transaction) error {
			if _, err := s.verifyClaim(ctx, tr, work); err != nil {
				return err
			}

			tr.Clear(
				fdb.Key(
					searchClaimKey(
						string(work.Class),
						searchBucket(work.OrgID, work.NodeID),
						work.OrgID,
						work.NodeID,
					),
				),
			)

			return nil
		},
	)
	if err != nil {
		return searchStorageError(
			ctx,
			"search.work.yield_failed",
			"yield search work claim",
			work.NodeID,
			err,
		)
	}

	return nil
}

// Release records one failure and delays the next claim of the same work.
//
// The work stays pending in every class and keeps retrying. Release logs the
// work item once when a counted failure sets its attempt count to
// searchFailureLimit.
//
//   - A counted failure contributes to the attempt count.
//   - An uncounted failure preserves the attempt count.
func (s *SearchWorkStore) Release(
	ctx context.Context,
	work searchdomain.Work,
	failure searchdomain.Failure,
) (err error) {
	defer telemetry.FDBOp(ctx, "store.search_work.release")(&err)
	var outcome failureOutcome
	err = transactSearch(
		ctx,
		s.db,
		func(tr fdb.Transaction) error {
			_, err := s.verifyClaim(ctx, tr, work)
			if err != nil {
				return err
			}

			outcome, err = s.recordFailure(ctx, tr, work, failure)

			return err
		},
	)
	if err != nil {
		return searchStorageError(
			ctx,
			"search.work.release_failed",
			"release failed search work",
			work.NodeID,
			err,
		)
	}

	logFailureOutcome(ctx, work, outcome, failure.Message)

	return nil
}

// clearSearchWork removes the finished record, its class-age entry, claim,
// checkpoint, error, and attempt count of work's class. record is the
// pending record the claim verification read.
func clearSearchWork(tr fdb.Transaction, work searchdomain.Work, record searchWorkRecord) {
	class := string(work.Class)
	bucket := searchBucket(work.OrgID, work.NodeID)
	removeSearchWork(tr, work.Class, record)
	tr.Clear(fdb.Key(searchClaimKey(class, bucket, work.OrgID, work.NodeID)))
	tr.Clear(fdb.Key(searchCursorKey(class, work.OrgID, work.NodeID)))
	tr.Clear(fdb.Key(searchErrorKey(class, work.OrgID, work.NodeID)))
	tr.Clear(fdb.Key(searchAttemptKey(class, work.OrgID, work.NodeID)))
	clearPendingWrites(tr, work.Class, work.OrgID, work.NodeID)
}
