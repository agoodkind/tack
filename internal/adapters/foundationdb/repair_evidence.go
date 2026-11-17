package foundationdb

import (
	"context"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/telemetry"
)

// RepairEvidenceRequest specifies which node records a repair reads.
type RepairEvidenceRequest struct {
	NodeID uuid.UUID
	// ParentID selects the first node in the ancestor chain.
	// [uuid.Nil] uses the repaired node's current child_of parent.
	ParentID uuid.UUID
	// Related specifies additional node IDs to read, such as targets of new edges.
	Related []uuid.UUID
}

// RepairNodeRecord records a node's stored state for a repair.
type RepairNodeRecord struct {
	NodeID   uuid.UUID
	Found    bool
	OrgID    uuid.UUID
	NodeType string
	// Primary contains the primary record's exact stored bytes.
	Primary []byte
	// ParentID is the sole child_of target. Roots and ambiguous parents use
	// [uuid.Nil]; Outgoing preserves every edge for an explicit repair.
	ParentID uuid.UUID
}

// RepairEdgeRecord identifies a stored relationship to or from the repaired node.
type RepairEdgeRecord struct {
	SourceID     uuid.UUID
	RelationType string
	TargetID     uuid.UUID
}

// RepairEvidence contains the source state that a repair reads in one transaction.
type RepairEvidence struct {
	Node     RepairNodeRecord
	Outgoing []RepairEdgeRecord
	Incoming []RepairEdgeRecord
	// Related contains records for edge ends, requested related nodes, and
	// ancestors, sorted by node ID without duplicates.
	Related []RepairNodeRecord
	// Chain lists ancestor node IDs, beginning with the chain start.
	Chain []uuid.UUID
	// Cycle reports whether the ancestor traversal encounters a repeated node
	// or the repaired node.
	Cycle bool
	// MetadataEpoch records the metadata epoch counter for the node's organization.
	MetadataEpoch int64
}

// RepairEvidence reads the node's resolution, exact primary record,
// edges in both directions, organization metadata epoch, and child_of ancestor
// chain in one read transaction. It also reads primary records for edge ends,
// requested related nodes, and ancestors within that transaction.
// It returns an error that wraps [domain.ErrFailedPrecondition] if the edges
// in either direction exceed one page.
func (s *RepairStore) RepairEvidence(
	ctx context.Context,
	request RepairEvidenceRequest,
) (evidence RepairEvidence, err error) {
	defer telemetry.FDBOp(ctx, "store.repair.evidence")(&err)
	err = runNodeReadTransaction(
		ctx,
		s.db,
		"read repair evidence of node "+request.NodeID.String(),
		func(tr fdb.Transaction) error {
			var readErr error
			evidence, readErr = readRepairEvidence(ctx, tr, request)

			return readErr
		},
	)

	return evidence, err
}

func readRepairEvidence(
	ctx context.Context,
	tr fdb.Transaction,
	request RepairEvidenceRequest,
) (RepairEvidence, error) {
	evidence := RepairEvidence{
		Node: RepairNodeRecord{
			NodeID:   request.NodeID,
			Found:    false,
			OrgID:    uuid.Nil,
			NodeType: "",
			Primary:  nil,
			ParentID: uuid.Nil,
		},
		Outgoing:      nil,
		Incoming:      nil,
		Related:       nil,
		Chain:         nil,
		Cycle:         false,
		MetadataEpoch: 0,
	}
	current, err := readRepairNode(ctx, tr, request.NodeID)
	if err != nil || !current.Found {
		return evidence, err
	}

	evidence.Node = current
	evidence.Outgoing, err = readRepairEdges(ctx, tr, current, false)
	if err != nil {
		return evidence, err
	}

	evidence.Incoming, err = readRepairEdges(ctx, tr, current, true)
	if err != nil {
		return evidence, err
	}

	evidence.MetadataEpoch, err = readSearchCounter(ctx, tr, searchEpochKey(current.OrgID))
	if err != nil {
		return evidence, err
	}

	related := newRepairRelatedSet()

	for _, edge := range evidence.Outgoing {
		related.add(edge.TargetID)
	}

	for _, edge := range evidence.Incoming {
		related.add(edge.SourceID)
	}

	for _, nodeID := range request.Related {
		related.add(nodeID)
	}

	start := request.ParentID
	if start == uuid.Nil {
		start = current.ParentID
	}

	evidence.Chain, evidence.Cycle, err = readRepairChain(ctx, tr, request.NodeID, start, related)
	if err != nil {
		return evidence, err
	}

	evidence.Related, err = related.read(ctx, tr)

	return evidence, err
}

func readRepairChain(
	ctx context.Context,
	tr fdb.Transaction,
	nodeID uuid.UUID,
	start uuid.UUID,
	related *repairRelatedSet,
) ([]uuid.UUID, bool, error) {
	chain := []uuid.UUID{}
	visited := map[uuid.UUID]bool{nodeID: true}

	for ancestorID := start; ancestorID != uuid.Nil; {
		if visited[ancestorID] {
			return chain, true, nil
		}

		visited[ancestorID] = true
		chain = append(chain, ancestorID)
		ancestor, err := readRepairNode(ctx, tr, ancestorID)
		if err != nil {
			return chain, false, err
		}

		related.store(ancestor)
		if !ancestor.Found {
			return chain, false, nil
		}

		ancestorID, err = readGraphParent(ctx, tr, ancestor.OrgID, ancestor.NodeID)
		if err != nil {
			return chain, false, err
		}
	}

	return chain, false, nil
}
