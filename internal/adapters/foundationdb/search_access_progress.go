package foundationdb

import (
	"context"
	"slices"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/searchaccess"
	"goodkind.io/tack/internal/telemetry"
)

// BeginAccess records the compiled access of one access work item before
// any page update is sent. When the compiled access differs from the
// recorded access, BeginAccess records it, marks every current page and
// every dependent pending, and restarts the pages phase at the first issued
// document. When pages are pending, the pages phase continues at the
// checkpoint. When only dependents are pending, the dependents phase starts.
// When nothing is pending, the work finishes.
func (s *SearchWorkStore) BeginAccess(ctx context.Context, work searchdomain.Work, compiled node.SearchAccess) (plan searchdomain.AccessPlan, err error) {
	defer telemetry.FDBOp(ctx, "store.search_work.begin_access")(&err)
	plan = searchdomain.AccessPlan{Work: work, Finished: false}
	err = transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		record, err := s.verifyClaim(ctx, tr, work)
		if err != nil {
			return err
		}
		state, err := accessRecordFor(ctx, tr, work.OrgID, work.NodeID)
		if err != nil {
			return err
		}
		next := work
		switch {
		case !sameAccess(state.Access, compiled):
			state = searchAccessRecord{Access: compiled, PagesPending: true, DependentsPending: true}
			state.Access.Generation = work.Generation
			if err := writeSearchRecord(ctx, tr, searchAccessKey(work.OrgID, work.NodeID), state); err != nil {
				return err
			}
			next.Phase, next.Cursor = searchdomain.PhasePages, ""
		case state.PagesPending:
			return nil
		case state.DependentsPending:
			next.Phase, next.Cursor = searchdomain.PhaseDependents, ""
		default:
			clearSearchWork(tr, work, record)
			plan = searchdomain.AccessPlan{Work: work, Finished: true}
			return nil
		}
		plan = searchdomain.AccessPlan{Work: next, Finished: false}
		return writeAccessProgress(ctx, tr, next, record)
	})
	if err != nil {
		return searchdomain.AccessPlan{Work: work, Finished: false}, searchStorageError(ctx, "search.access.begin_failed", "record compiled access", work.NodeID, err)
	}
	return plan, nil
}

// CompleteAccess checkpoints the accepted prefix of one access-only batch.
// After the last page it clears the pending-pages mark. Pending dependents
// then start the dependents phase. Otherwise the work finishes.
func (s *SearchWorkStore) CompleteAccess(ctx context.Context, work searchdomain.Work, checkpoint searchdomain.AccessCheckpoint) (err error) {
	defer telemetry.FDBOp(ctx, "store.search_work.complete_access")(&err)
	next := work
	if count := len(checkpoint.Accepted); count > 0 {
		next.Cursor = issuedCursor(checkpoint.Accepted[count-1])
	}
	err = transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		record, err := s.verifyClaim(ctx, tr, work)
		if err != nil {
			return err
		}
		state, err := accessRecordFor(ctx, tr, work.OrgID, work.NodeID)
		if err != nil {
			return err
		}
		if !sameAccess(state.Access, checkpoint.Access) {
			return searchdomain.ErrWorkChanged
		}
		if !checkpoint.PagesDone {
			return writeAccessProgress(ctx, tr, next, record)
		}
		state.PagesPending = false
		if err := writeSearchRecord(ctx, tr, searchAccessKey(work.OrgID, work.NodeID), state); err != nil {
			return err
		}
		if !state.DependentsPending {
			clearSearchWork(tr, work, record)
			return nil
		}
		next.Phase, next.Cursor = searchdomain.PhaseDependents, ""
		return writeAccessProgress(ctx, tr, next, record)
	})
	if err != nil {
		return searchStorageError(ctx, "search.access.checkpoint_failed", "checkpoint access updates", work.NodeID, err)
	}
	return nil
}

// writeAccessProgress stores the phase and cursor of access work.
func writeAccessProgress(ctx context.Context, tr fdb.Transaction, work searchdomain.Work, record searchWorkRecord) error {
	return writeSearchRecord(ctx, tr, searchCursorKey(string(work.Class), work.OrgID, work.NodeID), searchProgressRecord{
		Revision: record.Revision, Generation: record.Generation, Projection: "",
		Cursor: work.Cursor, Ordinal: 0, Phase: string(work.Phase),
	})
}

// sameAccess compares write versions and keys and ignores the generation.
// The generation orders writes and identifies no access decision.
func sameAccess(recorded, compiled node.SearchAccess) bool {
	return slices.Equal(recorded.Versions, compiled.Versions) && slices.Equal(recorded.Keys, compiled.Keys)
}

// accessRecordFor returns the recorded access state of one node. For a node
// without a record, it returns the stable policy version with no keys.
func accessRecordFor(ctx context.Context, tr fdb.Transaction, orgID, nodeID uuid.UUID) (searchAccessRecord, error) {
	var record searchAccessRecord
	found, err := readSearchRecord(ctx, tr, searchAccessKey(orgID, nodeID), &record)
	if err != nil || found {
		return record, err
	}
	return searchAccessRecord{
		Access:       node.SearchAccess{Versions: []string{searchaccess.StableVersion}, Keys: []string{}, Generation: 0},
		PagesPending: false, DependentsPending: false,
	}, nil
}

// scheduleAccessRepair marks the node's existing access record as having
// pending pages and schedules access work at a new generation. That access
// work rewrites every page of the node. A plain access schedule updates no
// page when the recorded access already equals the compiled access.
func scheduleAccessRepair(ctx context.Context, tr fdb.Transaction, now time.Time, orgID, nodeID uuid.UUID) error {
	var state searchAccessRecord
	found, err := readSearchRecord(ctx, tr, searchAccessKey(orgID, nodeID), &state)
	if err != nil {
		return err
	}
	if found {
		state.PagesPending = true
		if err := writeSearchRecord(ctx, tr, searchAccessKey(orgID, nodeID), state); err != nil {
			return err
		}
	}
	return scheduleExistingSearchChange(ctx, tr, now, orgID, nodeID, searchChangeAccess)
}

// accessStateFor returns the recorded write versions and keys of one node.
func accessStateFor(ctx context.Context, tr fdb.Transaction, orgID, nodeID uuid.UUID) (node.SearchAccess, error) {
	record, err := accessRecordFor(ctx, tr, orgID, nodeID)
	return record.Access, err
}
