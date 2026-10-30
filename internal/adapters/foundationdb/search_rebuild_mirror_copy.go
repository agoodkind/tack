package foundationdb

import (
	"context"
	"strconv"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// RecordMirrorMiss records a missed mirror write without verifying the claim of intent.
// RecordMirrorMiss does not change state when intent.MirrorMissed is nil or its
// deadline is at or before now. Within one FoundationDB transaction, RecordMirrorMiss
// calls owedMirrorCopy and stops when owedMirrorCopy returns false.
//
// When the replacement is in the switching state with claims paused, RecordMirrorMiss
// stores intent.MirrorMissed as a live class pending write record for the replacement
// index. RecordMirrorMiss does not write a copy item in that state. After the switch,
// live claims use the replacement index as Target. The next live registration of the
// document reads that record and waits for its deadline.
//
// In every other state, RecordMirrorMiss calls scheduleMirrorCopy with the current
// node generation. Copy work determines the lifetime of the copy class pending write
// record. The next live registration of the document reads that record through
// heldMirrorWrite and writes the page only to the serving index until the deadline
// passes.
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

// owedMirrorCopy returns false when the current node revision differs from revision,
// the node does not have pending live work, or the replacement mirror of live claims
// differs from intent.MirrorMissed.Index. A newer revision, removed live work, or a
// finished or failed replacement ends the need for the page in that index.
//
// owedMirrorCopy returns the claim scope that it reads. RecordMirrorMiss reads the
// replacement state from that scope. When the copy remains owed, owedMirrorCopy sets
// intent.Work.Generation to the current node generation and returns the updated
// intent with true.
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

// scheduleMirrorCopy runs inside the CompletePage transaction when the serving index
// accepts a page and the replacement index misses it.
// It first stores intent.MirrorMissed as a copy class pending write record
// when the deadline is after now. An existing pending copy does not prevent this write.
// scheduleMirrorCopy then skips the copy work item and checkpoint writes when copyCovers
// reports a pending copy of the same generation and revision that starts at the missed
// page or earlier. Otherwise, it writes a copy work item at the work generation and page
// revision. It also writes a copy checkpoint at the missed page with work.Cursor,
// work.Projection, the missed ordinal, and the pages phase.
// The copy item writes only the replacement index.
// scheduleMirrorCopy does not clear pending write records for other documents.
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

// copyCovers returns false when existing differs from generation or revision.
// copyCovers returns true when existing does not have a checkpoint.
// copyCovers also returns true when the checkpoint generation or revision differs from existing.
// readProgress starts such a copy at the first page.
// Otherwise, copyCovers returns whether the checkpoint ordinal is at or below ordinal.
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
