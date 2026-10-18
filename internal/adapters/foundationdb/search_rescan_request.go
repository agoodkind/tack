package foundationdb

import (
	"context"
	"encoding/hex"
	"slices"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// These constants identify the rescan phases. Each pass first indexes the
// organization's property definitions and node types, then reads its nodes.
const (
	scanDefinitions = "definitions"
	scanTypes       = "types"
	scanNodes       = "nodes"
)

// maxRescanNames limits each set of property names, type keys, and features
// in one content rescan. If any set exceeds the limit, the scan schedules
// content work for every node.
const maxRescanNames = 64

// searchRescanRequest selects work for one organization rescan.
// AllContent selects content work for every node. Names selects content
// work for nodes with a value under one of the property names. TypeKeys and
// Features select content work for nodes of those types with a value under
// a definition that applies to one of those features. Access selects access
// work for every node. Projection requests digest recomputation during the
// metadata pass and activation of this writer's renderer and format.
type searchRescanRequest struct {
	AllContent bool
	Names      []string
	TypeKeys   []string
	Features   []string
	Access     bool
	Projection bool
}

// searchProjectionTarget records the projection recomputation for one
// metadata pass. Sum stores the hex sum of contributions the pass has read.
// Epoch records the organization's metadata epoch when the pass started.
type searchProjectionTarget struct {
	Renderer int    `json:"renderer"`
	Format   int    `json:"format"`
	Sum      string `json:"sum"`
	Epoch    int64  `json:"epoch"`
}

// requestSearchRescan records a rescan of every node in orgID inside the
// caller's transaction. The rescan processes bounded pages. A request
// without projection recomputation keeps the scan cursor. During the node
// pass, it records the cursor as Stop. After reading the last node, the
// pass restarts at the first node and ends at Stop. A projection request
// restarts the metadata pass. The following node pass starts at Stop.
// The scan reads every node after the latest request.
func requestSearchRescan(ctx context.Context, tr fdb.Transaction, now time.Time, orgID uuid.UUID, request searchRescanRequest) error {
	generation, err := incrementSearchCounter(ctx, tr, searchScanKey(orgID))
	if err != nil {
		return err
	}
	class := searchdomain.WorkClassRescan
	existing, found, err := readSearchWork(ctx, tr, class, orgID, uuid.Nil)
	if err != nil {
		return err
	}
	state := searchScanRecord{
		Phase: scanDefinitions, Cursor: "", Stop: "", Wrapped: false, Content: false,
		Names: nil, TypeKeys: nil, Features: nil, Access: false, Projection: nil,
	}
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
	state = mergeScanRequest(state, request)
	if request.Projection {
		epoch, epochErr := readSearchCounter(ctx, tr, searchEpochKey(orgID))
		if epochErr != nil {
			return epochErr
		}
		state = restartProjectionPass(state, epoch)
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

// restartProjectionPass restarts state's metadata pass with a zero sum
// for this writer's renderer and format.
func restartProjectionPass(state searchScanRecord, epoch int64) searchScanRecord {
	state.Phase, state.Cursor = scanDefinitions, ""
	state.Projection = &searchProjectionTarget{
		Renderer: node.SearchRendererVersion, Format: searchProjectionFormat,
		Sum: hex.EncodeToString(sumContributions(nil, nil, nil)), Epoch: epoch,
	}
	return state
}

// allContent reports whether state schedules content work for every node.
func allContent(state searchScanRecord) bool {
	return state.Content && len(state.Names) == 0 && len(state.TypeKeys) == 0
}

// mergeScanRequest adds one request's content selection and access flag to
// the scan state. A request for all content clears Names, TypeKeys, and
// Features. If any selection set exceeds maxRescanNames, it selects content
// work for every node.
func mergeScanRequest(state searchScanRecord, request searchRescanRequest) searchScanRecord {
	state.Access = state.Access || request.Access
	selective := len(request.Names) > 0 || len(request.TypeKeys) > 0 && len(request.Features) > 0
	switch {
	case request.AllContent || allContent(state):
		return selectAllContent(state)
	case !selective:
		return state
	}
	state.Content = true
	state.Names = mergeNames(state.Names, request.Names)
	if len(request.Features) > 0 {
		state.TypeKeys = mergeNames(state.TypeKeys, request.TypeKeys)
		state.Features = mergeNames(state.Features, request.Features)
	}
	if len(state.Names) > maxRescanNames || len(state.TypeKeys) > maxRescanNames || len(state.Features) > maxRescanNames {
		return selectAllContent(state)
	}
	return state
}

func selectAllContent(state searchScanRecord) searchScanRecord {
	state.Content = true
	state.Names, state.TypeKeys, state.Features = nil, nil, nil
	return state
}

// mergeNames returns the sorted distinct values from current and added.
func mergeNames(current, added []string) []string {
	merged := slices.Clone(current)
	for _, name := range added {
		if name != "" && !slices.Contains(merged, name) {
			merged = append(merged, name)
		}
	}
	slices.Sort(merged)
	return merged
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
