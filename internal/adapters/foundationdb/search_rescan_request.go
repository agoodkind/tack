package foundationdb

import (
	"context"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// These constants identify the rescan phases. Each pass first indexes the
// organization's property definitions and node types, then reads its nodes.
const (
	scanDefinitions = "definitions"
	scanTypes       = "types"
	scanNodes       = "nodes"
)

// requestSearchRescan records one bounded rescan of every node in orgID
// inside the caller's metadata transaction. content schedules content work
// for each node, and access schedules access work. A request during a scan
// leaves the scan cursor unchanged. During the node pass it records the
// cursor as Stop. The pass then restarts at the first node after the last
// node and ends at Stop. The pass reads every node after the latest request.
func requestSearchRescan(ctx context.Context, tr fdb.Transaction, now time.Time, orgID uuid.UUID, content, access bool) error {
	generation, err := incrementSearchCounter(ctx, tr, searchScanKey(orgID))
	if err != nil {
		return err
	}
	class := searchdomain.WorkClassRescan
	existing, found, err := readSearchWork(ctx, tr, class, orgID, uuid.Nil)
	if err != nil {
		return err
	}
	state := searchScanRecord{Phase: scanDefinitions, Cursor: "", Stop: "", Wrapped: false, Content: content, Access: access}
	var previous *searchWorkRecord
	if found {
		previous = &existing
		stored, stateFound, readErr := readScanState(ctx, tr, orgID)
		if readErr != nil {
			return readErr
		}
		if stateFound {
			state = mergeScanRequest(stored, content, access)
		}
	}
	if err := writeSearchRecord(ctx, tr, searchCursorKey(string(class), orgID, uuid.Nil), state); err != nil {
		return err
	}
	record := searchWorkRecord{
		OrgID: orgID, NodeID: uuid.Nil, Generation: generation, Revision: 0,
		Deleted: false, EnqueuedAt: now.UTC(),
	}
	return writeSearchWork(ctx, tr, class, record, previous)
}

// mergeScanRequest adds one request to a scan in progress.
func mergeScanRequest(state searchScanRecord, content, access bool) searchScanRecord {
	state.Content = state.Content || content
	state.Access = state.Access || access
	if state.Phase == scanNodes {
		state.Stop = state.Cursor
		state.Wrapped = false
	}
	return state
}

// rescanProgress maps the scan state to the claim's phase and cursor. The
// metadata phases report PhaseMetadata. The node pass reports PhasePages.
func rescanProgress(ctx context.Context, tr fdb.Transaction, record searchWorkRecord, initial searchProgressRecord) (searchProgressRecord, error) {
	state, found, err := readScanState(ctx, tr, record.OrgID)
	if err != nil || !found {
		return initial, err
	}
	initial.Cursor = state.Cursor
	if state.Phase != scanNodes {
		initial.Phase = string(searchdomain.PhaseMetadata)
	}
	return initial, nil
}

func readScanState(ctx context.Context, tr fdb.Transaction, orgID uuid.UUID) (searchScanRecord, bool, error) {
	var state searchScanRecord
	found, err := readSearchRecord(ctx, tr, searchCursorKey(string(searchdomain.WorkClassRescan), orgID, uuid.Nil), &state)
	return state, found, err
}
