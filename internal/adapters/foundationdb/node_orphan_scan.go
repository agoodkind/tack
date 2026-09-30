package foundationdb

import (
	"context"
	"encoding/base64"
	"encoding/json"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// orphanScanPageSize bounds the resolution records that one orphan scan
// transaction reads.
const orphanScanPageSize = 100

// OrphanNode identifies one node of a type that lists CanLiveUnder types and
// has no child_of edge to an existing node that NodeType metadata places
// above it.
type OrphanNode struct {
	ID       uuid.UUID `json:"id"`
	OrgID    uuid.UUID `json:"org_id"`
	NodeType string    `json:"node_type"`
}

// ParentEdge identifies one child_of edge from NodeID to TargetID that does
// not target the node's parent_id, on a node that also has a child_of edge
// to its parent_id.
type ParentEdge struct {
	NodeID   uuid.UUID `json:"node_id"`
	OrgID    uuid.UUID `json:"org_id"`
	TargetID uuid.UUID `json:"target_id"`
}

// OrphanPage reports one page of the orphan scan. Scanned counts the
// resolution records the page read. Next is the cursor of the following
// page, or empty after the last page.
type OrphanPage struct {
	Scanned          int
	Orphans          []OrphanNode
	ExtraParentEdges []ParentEdge
	Next             string
}

// orphanCandidate is one scanned node of a type that requires a parent.
type orphanCandidate struct {
	node     OrphanNode
	kind     *node.NodeType
	parentID uuid.UUID
}

// ScanOrphans reads one page of node resolution records of every
// organization after cursor. It returns the nodes without a hierarchy parent
// and the child_of edges that do not target the parent_id of a node with a
// hierarchy parent. One read transaction reads the page, the node types, and
// the parent_id of each node. Each candidate then reads its child_of edges in
// bounded pages, one read transaction per page.
func (s *NodeDeleteStore) ScanOrphans(ctx context.Context, cursor string) (page OrphanPage, err error) {
	defer telemetry.FDBOp(ctx, "store.node.orphan_scan")(&err)
	var candidates []orphanCandidate
	err = runNodeReadTransaction(ctx, s.nodes.db, "scan orphan nodes", func(tr fdb.Transaction) error {
		var readErr error
		page, candidates, readErr = readOrphanCandidates(ctx, tr, cursor)
		return readErr
	})
	empty := OrphanPage{Scanned: 0, Orphans: nil, ExtraParentEdges: nil, Next: ""}
	if err != nil {
		return empty, err
	}
	page.Orphans, page.ExtraParentEdges = []OrphanNode{}, []ParentEdge{}
	for _, candidate := range candidates {
		targets, hasParent, parentErr := s.readParentEdges(ctx, candidate)
		if parentErr != nil {
			return empty, parentErr
		}
		if !hasParent {
			page.Orphans = append(page.Orphans, candidate.node)
			continue
		}
		page.ExtraParentEdges = append(page.ExtraParentEdges, extraParentEdges(candidate, targets)...)
	}
	return page, nil
}

// readOrphanCandidates reads up to orphanScanPageSize resolution records
// after cursor and returns the nodes of a type that lists CanLiveUnder types,
// with the parent_id of each.
func readOrphanCandidates(ctx context.Context, tr fdb.Transaction, cursor string) (OrphanPage, []orphanCandidate, error) {
	keyRange, err := fdb.PrefixRange(withPrefix(tuple.Tuple{keyNodeResolve}.Pack()))
	if err != nil {
		return OrphanPage{}, nil, nodeOperationFailure(ctx, "create node resolution range", err)
	}
	begin := fdb.FirstGreaterOrEqual(keyRange.Begin)
	if cursor != "" {
		lastKey, decodeErr := base64.RawURLEncoding.DecodeString(cursor)
		if decodeErr != nil {
			return OrphanPage{}, nil, nodeOperationFailure(ctx, "decode orphan scan cursor", decodeErr)
		}
		begin = fdb.FirstGreaterThan(fdb.Key(lastKey))
	}
	selection := fdb.SelectorRange{Begin: begin, End: fdb.FirstGreaterOrEqual(keyRange.End)}
	items, err := tr.GetRange(selection, fdb.RangeOptions{Limit: orphanScanPageSize + 1}).GetSliceWithError()
	if err != nil {
		return OrphanPage{}, nil, searchReadFailure(ctx, "read node resolution page", err)
	}
	page := OrphanPage{Scanned: min(len(items), orphanScanPageSize), Orphans: nil, ExtraParentEdges: nil, Next: ""}
	if len(items) > orphanScanPageSize {
		items = items[:orphanScanPageSize]
		page.Next = base64.RawURLEncoding.EncodeToString(items[len(items)-1].Key)
	}
	kindsByOrg := make(map[uuid.UUID]map[string]*node.NodeType)
	candidates := make([]orphanCandidate, 0, len(items))
	for _, item := range items {
		candidate, required, err := decodeOrphanCandidate(ctx, tr, item, kindsByOrg)
		if err != nil {
			return OrphanPage{}, nil, err
		}
		if required {
			candidates = append(candidates, candidate)
		}
	}
	return page, candidates, readCandidateParentIDs(ctx, tr, candidates)
}

// decodeOrphanCandidate decodes one resolution record and reads its node
// type. It reports whether the type lists CanLiveUnder types.
func decodeOrphanCandidate(
	ctx context.Context, tr fdb.Transaction, item fdb.KeyValue, kindsByOrg map[uuid.UUID]map[string]*node.NodeType,
) (orphanCandidate, bool, error) {
	none := orphanCandidate{node: OrphanNode{ID: uuid.Nil, OrgID: uuid.Nil, NodeType: ""}, kind: nil, parentID: uuid.Nil}
	nodeID, err := lastTupleID(ctx, item.Key)
	if err != nil {
		return none, false, err
	}
	var resolved node.NodeResolve
	if err := json.Unmarshal(item.Value, &resolved); err != nil {
		return none, false, nodeOperationFailure(ctx, "decode node resolution "+nodeID.String(), err)
	}
	if kindsByOrg[resolved.OrgID] == nil {
		kindsByOrg[resolved.OrgID] = make(map[string]*node.NodeType)
	}
	kind, err := cachedNodeTypeByKey(ctx, tr, resolved.OrgID, resolved.NodeType, kindsByOrg[resolved.OrgID])
	if err != nil || kind == nil || len(kind.CanLiveUnder) == 0 {
		return none, false, err
	}
	return orphanCandidate{
		node: OrphanNode{ID: nodeID, OrgID: resolved.OrgID, NodeType: resolved.NodeType}, kind: kind, parentID: uuid.Nil,
	}, true, nil
}
