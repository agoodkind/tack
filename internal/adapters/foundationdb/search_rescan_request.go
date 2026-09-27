package foundationdb

import (
	"context"
	"slices"
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

// maxRescanNames bounds the property names one content rescan records. A
// larger set schedules content work for every node.
const maxRescanNames = 64

// requestSearchRescan records one bounded rescan of every node in orgID
// inside the caller's metadata transaction. names schedules content work
// for each node that has a value under one of the names, and access
// schedules access work for every node. A request during a scan leaves the
// scan cursor unchanged. During the node pass it records the cursor as
// Stop. The pass then restarts at the first node after the last node and
// ends at Stop. The pass reads every node after the latest request.
func requestSearchRescan(ctx context.Context, tr fdb.Transaction, now time.Time, orgID uuid.UUID, names []string, access bool) error {
	generation, err := incrementSearchCounter(ctx, tr, searchScanKey(orgID))
	if err != nil {
		return err
	}
	class := searchdomain.WorkClassRescan
	existing, found, err := readSearchWork(ctx, tr, class, orgID, uuid.Nil)
	if err != nil {
		return err
	}
	state := searchScanRecord{Phase: scanDefinitions, Cursor: "", Stop: "", Wrapped: false, Content: false, Names: nil, Access: false}
	var previous *searchWorkRecord
	if found {
		previous = &existing
		stored, stateFound, readErr := readScanState(ctx, tr, orgID)
		if readErr != nil {
			return readErr
		}
		if stateFound {
			state = stored
			if state.Phase == scanNodes {
				state.Stop = state.Cursor
				state.Wrapped = false
			}
		}
	}
	state = mergeScanRequest(state, names, access)
	if err := writeSearchRecord(ctx, tr, searchCursorKey(string(class), orgID, uuid.Nil), state); err != nil {
		return err
	}
	record := searchWorkRecord{
		OrgID: orgID, NodeID: uuid.Nil, Generation: generation, Revision: 0,
		Deleted: false, EnqueuedAt: now.UTC(),
	}
	return writeSearchWork(ctx, tr, class, record, previous)
}

// mergeScanRequest adds one request's content names and access flag to the
// scan state. Content with no names selects every node. When the merged
// names exceed maxRescanNames, the state stores no names and the scan
// schedules content work for every node. That scan performs a superset of
// the requested work, and the stored state stays within its bound.
func mergeScanRequest(state searchScanRecord, names []string, access bool) searchScanRecord {
	state.Access = state.Access || access
	if len(names) == 0 {
		return state
	}
	switch {
	case state.Content && len(state.Names) == 0:
		return state
	case !state.Content:
		state.Content = true
		state.Names = slices.Clone(names)
	default:
		for _, name := range names {
			if !slices.Contains(state.Names, name) {
				state.Names = append(state.Names, name)
			}
		}
	}
	if len(state.Names) > maxRescanNames {
		state.Names = nil
	}
	slices.Sort(state.Names)
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
