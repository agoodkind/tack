package foundationdb

import (
	"context"
	"strconv"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// RecordMirrorMiss preserves an unexpired mirror deadline without requiring the
// original claim. A changed revision, absent live work, or different replacement
// ends the copy obligation.
// During paused Switching, save the deadline as live pending state without adding
// copy work after the final drain. Later live claims observe that deadline whether
// the replacement remains a mirror or becomes the serving index.
// Outside that pause, the pending copy retains the deadline.
func (s *SearchWorkStore) RecordMirrorMiss(ctx context.Context, intent searchdomain.WriteIntent) (err error) {
	defer telemetry.FDBOp(ctx, "store.search_work.record_mirror_miss")(&err)
	now := s.clock.Now()
	if intent.MirrorMissed == nil || !now.Before(intent.MirrorMissed.Deadline) {
		return nil
	}
	revision, err := strconv.ParseInt(intent.Page.Revision, 10, 64)
	if err != nil {
		return searchStorageError(ctx, "search.work.revision_invalid", "parse missed page revision", intent.Work.NodeID, err)
	}
	err = transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		current, scope, owed, err := owedMirrorCopy(ctx, tr, intent, revision)
		if err != nil || !owed {
			return err
		}
		if scope.rebuild.State == searchdomain.RebuildSwitching && scope.rebuild.Paused {
			return recordPendingWrite(ctx, tr, current.Work, *current.MirrorMissed)
		}
		return scheduleMirrorCopy(ctx, tr, now.UTC(), current, revision)
	})
	if err != nil {
		return searchStorageError(ctx, "search.work.mirror_miss_failed", "record missed mirror write", intent.Work.NodeID, err)
	}
	return nil
}

// An expired claim can still owe a mirror write at the current node revision.
// Use the current generation when preserving that obligation after claim loss.
func owedMirrorCopy(ctx context.Context, tr fdb.Transaction, intent searchdomain.WriteIntent, revision int64) (searchdomain.WriteIntent, claimScope, bool, error) {
	work := intent.Work
	var scope claimScope
	current, err := readSearchCounter(ctx, tr, searchRevisionKey(work.OrgID, work.NodeID))
	if err != nil || current != revision {
		return intent, scope, false, err
	}
	if _, livePending, err := readSearchWork(ctx, tr, searchdomain.WorkClassLive, work.OrgID, work.NodeID); err != nil || !livePending {
		return intent, scope, false, err
	}
	scope, err = readClaimScope(ctx, tr)
	if err != nil {
		return intent, scope, false, err
	}
	if _, mirror, _ := scope.targets(searchdomain.WorkClassLive); mirror != intent.MirrorMissed.Index {
		return intent, scope, false, nil
	}
	generation, err := readSearchCounter(ctx, tr, searchGenerationKey(work.OrgID, work.NodeID))
	if err != nil {
		return intent, scope, false, err
	}
	intent.Work.Generation = generation
	return intent, scope, true, nil
}

// Commit owed replacement work in the serving checkpoint transaction.
// Preserve an existing copy that starts at the missed page or an earlier page.
// Save every unexpired document/index deadline even when that copy already exists.
// A new copy starts with the cursor before the missed page.
func scheduleMirrorCopy(ctx context.Context, tr fdb.Transaction, now time.Time, intent searchdomain.WriteIntent, revision int64) error {
	work, missed := intent.Work, *intent.MirrorMissed
	if now.Before(missed.Deadline) {
		copyWork := work
		copyWork.Class = searchdomain.WorkClassCopy
		if err := recordPendingWrite(ctx, tr, copyWork, missed); err != nil {
			return err
		}
	}
	existing, found, err := readSearchWork(ctx, tr, searchdomain.WorkClassCopy, work.OrgID, work.NodeID)
	if err != nil {
		return err
	}
	var previous *searchWorkRecord
	if found {
		covered, coverErr := copyCovers(ctx, tr, existing, work.Generation, revision, missed.Ordinal)
		if coverErr != nil || covered {
			return coverErr
		}
		previous = &existing
	}
	if err := writeSearchWork(ctx, tr, searchdomain.WorkClassCopy, searchWorkRecord{
		OrgID: work.OrgID, NodeID: work.NodeID, Generation: work.Generation, Revision: revision, Deleted: false, EnqueuedAt: now,
	}, previous); err != nil {
		return err
	}
	return writeSearchRecord(ctx, tr, searchCursorKey(string(searchdomain.WorkClassCopy), work.OrgID, work.NodeID), searchProgressRecord{
		Revision: revision, Generation: work.Generation, Projection: work.Projection,
		Cursor: work.Cursor, Ordinal: missed.Ordinal, Phase: string(searchdomain.PhasePages),
	})
}

// copyCovers reports whether an existing copy item already writes the missed page
// ordinal. It returns false when the copy record has a different generation or
// revision. It returns true when the copy lacks a checkpoint or the checkpoint has
// a different generation or revision. readProgress restarts such a copy at page zero.
// Otherwise, it returns true when the checkpoint ordinal is at or before the missed
// ordinal.
func copyCovers(ctx context.Context, tr fdb.Transaction, existing searchWorkRecord, generation, revision int64, ordinal uint64) (bool, error) {
	if existing.Generation != generation || existing.Revision != revision {
		return false, nil
	}
	var progress searchProgressRecord
	started, err := readSearchRecord(ctx, tr, searchCursorKey(string(searchdomain.WorkClassCopy), existing.OrgID, existing.NodeID), &progress)
	if err != nil || !started {
		return true, err
	}
	stale := progress.Generation != existing.Generation || progress.Revision != existing.Revision
	return stale || progress.Ordinal <= ordinal, nil
}
