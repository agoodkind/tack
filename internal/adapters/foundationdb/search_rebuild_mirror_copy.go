package foundationdb

import (
	"context"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

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

// copyCovers reports whether existing schedules a copy of the same generation and
// revision from ordinal or an earlier page.
// A copy without a checkpoint starts at the first page and covers every ordinal.
func copyCovers(ctx context.Context, tr fdb.Transaction, existing searchWorkRecord, generation, revision int64, ordinal uint64) (bool, error) {
	if existing.Generation != generation || existing.Revision != revision {
		return false, nil
	}
	var progress searchProgressRecord
	started, err := readSearchRecord(ctx, tr, searchCursorKey(string(searchdomain.WorkClassCopy), existing.OrgID, existing.NodeID), &progress)
	if err != nil || !started {
		return true, err
	}
	return progress.Generation == generation && progress.Revision == revision && progress.Ordinal <= ordinal, nil
}
