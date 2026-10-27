package foundationdb

import (
	"context"
	"slices"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// rolloutRepair specifies the action a stale node needs before
// verification can pass. A larger value takes precedence for a node.
type rolloutRepair uint8

const (
	// rolloutRepairWait means the node has pending page work that
	// verification must wait for.
	rolloutRepairWait rolloutRepair = iota + 1
	// rolloutRepairAccess means a page does not store the exact write
	// versions or its access generation is below the node's recorded
	// generation. Access work rewrites every page of the node.
	rolloutRepairAccess
	// rolloutRepairContent means OpenSearch does not have an active page
	// document. Content work writes the node's pages.
	rolloutRepairContent
)

// rolloutNodeState records whether a node has pending page work and the
// node's recorded access generation.
type rolloutNodeState struct {
	pending    bool
	generation int64
}

// staleRolloutNodes returns the repair each node with a stale page needs
// in one verification batch. It reads each node's state once inside the
// transaction.
func staleRolloutNodes(
	ctx context.Context,
	tr fdb.Transaction,
	orgID uuid.UUID,
	documents []searchdomain.RolloutDocument,
	states map[string]searchdomain.DocumentAccess,
	required []string,
) (map[uuid.UUID]rolloutRepair, error) {
	stale := make(map[uuid.UUID]rolloutRepair)
	nodes := make(map[uuid.UUID]rolloutNodeState)
	for _, document := range documents {
		nodeState, read := nodes[document.NodeID]
		if !read {
			var err error
			nodeState, err = readRolloutNodeState(ctx, tr, orgID, document.NodeID)
			if err != nil {
				return nil, err
			}
			nodes[document.NodeID] = nodeState
		}
		repair := pageRepair(states[document.Document.DocumentID], required, nodeState)
		if repair > stale[document.NodeID] {
			stale[document.NodeID] = repair
		}
	}
	return stale, nil
}

// pageRepair returns the repair one page needs, or zero when the page is
// current.
func pageRepair(state searchdomain.DocumentAccess, required []string, nodeState rolloutNodeState) rolloutRepair {
	versions := slices.Clone(state.Versions)
	slices.Sort(versions)
	switch {
	case nodeState.pending:
		return rolloutRepairWait
	case !state.Found || state.Retired:
		return rolloutRepairContent
	case !slices.Equal(versions, required) || state.Generation < nodeState.generation:
		return rolloutRepairAccess
	default:
		return 0
	}
}

// readRolloutNodeState reads one node's pending live, access, cleanup,
// and copy work and recorded access generation inside tr.
func readRolloutNodeState(ctx context.Context, tr fdb.Transaction, orgID, nodeID uuid.UUID) (rolloutNodeState, error) {
	state := rolloutNodeState{pending: false, generation: 0}
	for _, class := range searchNodeClasses {
		_, found, err := readSearchWork(ctx, tr, class, orgID, nodeID)
		if err != nil {
			return state, err
		}
		state.pending = state.pending || found
	}
	record, err := accessRecordFor(ctx, tr, orgID, nodeID)
	if err != nil {
		return state, err
	}
	state.generation = record.Access.Generation
	return state, nil
}

// scheduleRolloutRepairs schedules content or access work for each stale
// node. It does not schedule new work for a waiting node.
func scheduleRolloutRepairs(ctx context.Context, tr fdb.Transaction, now time.Time, orgID uuid.UUID, stale map[uuid.UUID]rolloutRepair) error {
	for nodeID, repair := range stale {
		var err error
		switch repair {
		case rolloutRepairWait:
		case rolloutRepairContent:
			err = scheduleExistingSearchChange(ctx, tr, now, orgID, nodeID, searchChangeContent)
		case rolloutRepairAccess:
			err = scheduleAccessRepair(ctx, tr, now, orgID, nodeID)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
