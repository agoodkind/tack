package foundationdb

import (
	"context"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// ScheduleDependents schedules access work for one bounded page of
// dependents in one transaction and checkpoints the page cursor. For a
// deleted node the page lists recorded counterparts, and the transaction
// clears each scheduled counterpart record. The last page clears the
// pending-dependents mark, or finishes the deleted node's access class.
func (s *SearchWorkStore) ScheduleDependents(ctx context.Context, work searchdomain.Work, page node.IDPage) (err error) {
	defer telemetry.FDBOp(ctx, "store.search_work.schedule_dependents")(&err)
	err = transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		record, err := s.verifyClaim(ctx, tr, work)
		if err != nil {
			return err
		}
		now := s.clock.Now()
		for _, dependentID := range page.IDs {
			if err := scheduleExistingSearchChange(ctx, tr, now, work.OrgID, dependentID, searchChangeAccess); err != nil {
				return err
			}
			if record.Deleted {
				tr.Clear(fdb.Key(searchFanoutKey(work.OrgID, work.NodeID, dependentID)))
			}
		}
		if !page.Done {
			next := work
			next.Phase, next.Cursor = searchdomain.PhaseDependents, page.NextCursor
			return writeAccessProgress(ctx, tr, next, record)
		}
		if record.Deleted {
			return finishDeletedClass(ctx, tr, work, record)
		}
		state, err := accessRecordFor(ctx, tr, work.OrgID, work.NodeID)
		if err != nil {
			return err
		}
		state.DependentsPending = false
		if err := writeSearchRecord(ctx, tr, searchAccessKey(work.OrgID, work.NodeID), state); err != nil {
			return err
		}
		clearSearchWork(tr, work, record)
		return nil
	})
	if err != nil {
		return searchStorageError(ctx, "search.access.dependents_failed", "schedule access dependents", work.NodeID, err)
	}
	return nil
}
