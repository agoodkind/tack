package foundationdb

import (
	"context"
	"encoding/base64"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// affectedProgress records whether an affected pass step ran, the remaining
// set size after the step, and whether that size is a lower bound for the
// progress log.
type affectedProgress struct {
	ran       bool
	remaining int
	capped    bool
}

// affectedCheck records the repair one affected node needs, whether the
// batch covers all of its pages, and the cursor after its last page in the
// batch.
type affectedCheck struct {
	repair  rolloutRepair
	covered bool
	cursor  string
}

// completeAffected runs one affected pass step. An empty set activates the
// candidate and stops collection in the same transaction. Otherwise, the
// step verifies a batch of at most searchRolloutAffectedLimit nodes.
func completeAffected(
	ctx context.Context,
	tr fdb.Transaction,
	work searchdomain.Work,
	record *searchRolloutRecord,
	documents []searchdomain.RolloutDocument,
	states map[string]searchdomain.DocumentAccess,
	now time.Time,
) (searchdomain.RolloutStep, affectedProgress, error) {
	nodes, err := readRolloutAffected(ctx, tr, work.OrgID, record.Generation, searchRolloutAffectedLimit+1)
	if err != nil {
		return searchdomain.RolloutContinue, affectedProgress{ran: false, remaining: 0, capped: false}, err
	}
	if len(nodes) == 0 {
		record.PreviousVersion, record.ActiveVersion = record.ActiveVersion, record.CandidateVersion
		record.AffectedPass, record.VerifyCursor = false, ""
		return searchdomain.RolloutContinue, affectedProgress{ran: true, remaining: 0, capped: false},
			stopRolloutCollecting(ctx, tr, work.OrgID, record.Generation)
	}
	batch := nodes[:min(len(nodes), searchRolloutAffectedLimit)]
	step, removed, err := verifyAffectedBatch(ctx, tr, work, record, batch, documents, states, now)
	progress := affectedProgress{ran: true, remaining: len(nodes) - removed, capped: len(nodes) > searchRolloutAffectedLimit}
	return step, progress, err
}

// verifyAffectedBatch checks affected nodes in key order with
// checkAffectedNode and returns the step and the number of removed nodes. It
// keeps nodes with pending work or stale pages in the set and removes
// covered current nodes or nodes that no longer resolve after their work
// finishes. It stops at the first node the batch does not cover. For the
// first node, it saves the cursor after its last page when its pages are
// current and keeps the earlier cursor when the node waits or needs repair.
// It returns [searchdomain.RolloutWait] and schedules repairs when a checked
// node waits or needs repair. Otherwise, it returns
// [searchdomain.RolloutContinue].
func verifyAffectedBatch(
	ctx context.Context,
	tr fdb.Transaction,
	work searchdomain.Work,
	record *searchRolloutRecord,
	nodes []uuid.UUID,
	documents []searchdomain.RolloutDocument,
	states map[string]searchdomain.DocumentAccess,
	now time.Time,
) (searchdomain.RolloutStep, int, error) {
	byNode := make(map[uuid.UUID][]searchdomain.RolloutDocument, len(nodes))
	for _, document := range documents {
		byNode[document.NodeID] = append(byNode[document.NodeID], document)
	}
	firstCursor := record.VerifyCursor
	record.VerifyCursor = ""
	stale := make(map[uuid.UUID]rolloutRepair)
	removed := 0
	for position, nodeID := range nodes {
		cursor := ""
		if position == 0 {
			cursor = firstCursor
		}
		check, err := checkAffectedNode(ctx, tr, work, record, nodeID, byNode[nodeID], states, cursor)
		if err != nil {
			return searchdomain.RolloutContinue, removed, err
		}
		switch {
		case check.repair != 0:
			stale[nodeID] = check.repair
			if position == 0 {
				record.VerifyCursor = firstCursor
			}
		case check.covered:
			removeRolloutAffected(tr, work.OrgID, record.Generation, nodeID)
			removed++
		case position == 0:
			record.VerifyCursor = check.cursor
		}
		if !check.covered {
			break
		}
	}
	if len(stale) > 0 {
		return searchdomain.RolloutWait, removed, scheduleRolloutRepairs(ctx, tr, now, work.OrgID, stale)
	}
	return searchdomain.RolloutContinue, removed, nil
}

// checkAffectedNode reads a node's pending work and recorded access
// generation, then checks for an issued page after its last page in the
// batch. It marks the node covered when that read does not find another
// page. A node with pending live, access, cleanup, or copy work waits. After
// that work finishes, it reads the node resolution and marks a node that no
// longer resolves covered. For a node that still resolves, it checks each
// page in the batch for access or content repair.
func checkAffectedNode(
	ctx context.Context,
	tr fdb.Transaction,
	work searchdomain.Work,
	record *searchRolloutRecord,
	nodeID uuid.UUID,
	documents []searchdomain.RolloutDocument,
	states map[string]searchdomain.DocumentAccess,
	cursor string,
) (affectedCheck, error) {
	check := affectedCheck{repair: 0, covered: false, cursor: cursor}
	nodeState, err := readRolloutNodeState(ctx, tr, work.OrgID, nodeID)
	if err != nil {
		return check, err
	}
	if len(documents) > 0 {
		last := documents[len(documents)-1]
		check.cursor = base64.RawURLEncoding.EncodeToString(searchIssuedKey(work.OrgID, nodeID, last.Document.Revision, last.Document.Ordinal, last.Document.Projection))
	}
	keyRange, begin, err := affectedPageRange(ctx, work.OrgID, nodeID, check.cursor)
	if err != nil {
		return check, err
	}
	more, err := tr.GetRange(fdb.SelectorRange{Begin: begin, End: fdb.FirstGreaterOrEqual(keyRange.End)}, fdb.RangeOptions{Limit: 1}).GetSliceWithError()
	if err != nil {
		return check, searchReadFailure(ctx, "read issued documents of affected node "+nodeID.String(), err)
	}
	check.covered = len(more) == 0
	if nodeState.pending {
		check.repair = rolloutRepairWait
		return check, nil
	}
	resolved, err := tr.Get(fdb.Key(nodeResolveKey(nodeID))).Get()
	if err != nil {
		return check, searchReadFailure(ctx, "read node resolution of affected node "+nodeID.String(), err)
	}
	if len(resolved) == 0 {
		check.covered = true
		return check, nil
	}
	for _, document := range documents {
		if repair := pageRepair(states[document.Document.DocumentID], record.WriteVersions, nodeState); repair > check.repair {
			check.repair = repair
		}
	}
	return check, nil
}
