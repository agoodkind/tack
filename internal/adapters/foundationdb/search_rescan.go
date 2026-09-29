package foundationdb

import (
	"bytes"
	"context"
	"encoding/base64"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// CompleteRescan schedules the requested work for one bounded batch of
// scanned nodes in one transaction and checkpoints the scan. A pass with a
// Stop cursor restarts at the first node after the last node, and the
// restarted pass ends once it has read Stop.
func (s *SearchWorkStore) CompleteRescan(ctx context.Context, work searchdomain.Work, scan searchdomain.ScanResult) (err error) {
	defer telemetry.FDBOp(ctx, "store.search_work.complete_rescan")(&err)
	err = transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		record, err := s.verifyClaim(ctx, tr, work)
		if err != nil {
			return err
		}
		state, found, err := readScanState(ctx, tr, work.OrgID)
		if err != nil || !found || state.Phase != scanNodes || state.Cursor != work.Cursor {
			return claimMismatch(err)
		}
		now := s.clock.Now()
		affected, err := nodesWithNames(ctx, tr, scan.Nodes, state)
		if err != nil {
			return err
		}
		for _, nodeID := range scan.Nodes {
			if err := scheduleRescannedNode(ctx, tr, now, work.OrgID, nodeID, state, affected[nodeID]); err != nil {
				return err
			}
		}
		finished, err := advanceScan(ctx, &state, scan)
		if err != nil {
			return err
		}
		if finished {
			clearSearchWork(tr, work, record)
			return nil
		}
		return writeSearchRecord(ctx, tr, searchCursorKey(string(work.Class), work.OrgID, uuid.Nil), state)
	})
	if err != nil {
		return searchStorageError(ctx, "search.rescan.checkpoint_failed", "checkpoint search rescan of organization "+work.OrgID.String(), work.NodeID, err)
	}
	return nil
}

// scheduleRescannedNode schedules the content or access work the scan
// requested for one node. affected reports whether the node has a value
// under one of the scan's names.
func scheduleRescannedNode(ctx context.Context, tr fdb.Transaction, now time.Time, orgID, nodeID uuid.UUID, state searchScanRecord, affected bool) error {
	if state.Content && (len(state.Names) == 0 || affected) {
		if err := scheduleExistingSearchChange(ctx, tr, now, orgID, nodeID, searchChangeContent); err != nil {
			return err
		}
	}
	if state.Access {
		return scheduleExistingSearchChange(ctx, tr, now, orgID, nodeID, searchChangeAccess)
	}
	return nil
}

// advanceScan updates the scan checkpoint in state from scan and reports
// whether the rescan finished. A page that is not the last page sets the
// cursor to the page's next cursor. The last page of a first pass with a
// Stop cursor starts the wrapped pass at the first node.
func advanceScan(ctx context.Context, state *searchScanRecord, scan searchdomain.ScanResult) (bool, error) {
	if state.Wrapped {
		passedStop, err := scanPassedStop(ctx, scan.NextCursor, state.Stop)
		if err != nil {
			return false, err
		}
		if scan.Done || passedStop {
			return true, nil
		}
		state.Cursor = scan.NextCursor
		return false, nil
	}
	if !scan.Done {
		state.Cursor = scan.NextCursor
		return false, nil
	}
	if state.Stop == "" {
		return true, nil
	}
	state.Wrapped, state.Cursor = true, ""
	return false, nil
}

// scanPassedStop reports whether the last node key of a page sorts at or
// after stop. An empty next cursor marks a final page and reports true.
func scanPassedStop(ctx context.Context, next, stop string) (bool, error) {
	if next == "" {
		return true, nil
	}
	nextKey, err := base64.RawURLEncoding.DecodeString(next)
	if err != nil {
		return false, searchReadFailure(ctx, "decode scan cursor", err)
	}
	stopKey, err := base64.RawURLEncoding.DecodeString(stop)
	if err != nil {
		return false, searchReadFailure(ctx, "decode scan stop", err)
	}
	return bytes.Compare(nextKey, stopKey) >= 0, nil
}
