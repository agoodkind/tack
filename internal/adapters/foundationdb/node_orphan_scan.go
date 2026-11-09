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

// orphanScanPageSize limits the resolution records that one orphan scan page
// includes. The transaction reads one additional record to detect a
// following page.
const orphanScanPageSize = 100

// OrphanNode identifies a node with a type that lists CanLiveUnder types.
// The node does not have a child_of edge to another existing node in its
// organization.
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
// resolution records that the page includes. Next stores the cursor for the
// following page and contains an empty string after the last page.
type OrphanPage struct {
	Scanned          int
	Orphans          []OrphanNode
	ExtraParentEdges []ParentEdge
	Next             string
}

// orphanCandidate is one scanned node of a type that requires a parent.
type orphanCandidate struct {
	node     OrphanNode
	parentID uuid.UUID
}

// ScanOrphans reads a page of node resolution records across organizations
// after cursor.
//
// One read transaction reads the page, node types, and each candidate's
// parent_id. Each candidate's child_of scan uses bounded pages with one read
// transaction per page.
//
//   - Orphans contains nodes without a hierarchy parent.
//   - ExtraParentEdges contains edges that do not target parent_id on nodes
//     that have a hierarchy parent.
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

// readOrphanCandidates requests up to orphanScanPageSize + 1 resolution
// records after cursor to detect a following page. It includes at most
// orphanScanPageSize records in the page and returns the nodes of types that
// list CanLiveUnder types, with the parent_id of each.
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
	none := orphanCandidate{node: OrphanNode{ID: uuid.Nil, OrgID: uuid.Nil, NodeType: ""}, parentID: uuid.Nil}
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
		node: OrphanNode{ID: nodeID, OrgID: resolved.OrgID, NodeType: resolved.NodeType}, parentID: uuid.Nil,
	}, true, nil
}
