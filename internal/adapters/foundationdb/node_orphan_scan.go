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
// has no edge to an existing node that NodeType metadata places above it.
type OrphanNode struct {
	ID       uuid.UUID `json:"id"`
	OrgID    uuid.UUID `json:"org_id"`
	NodeType string    `json:"node_type"`
}

// OrphanPage reports one page of the orphan scan. Scanned counts the
// resolution records the page read. Next is the cursor of the following
// page, or empty after the last page.
type OrphanPage struct {
	Scanned int
	Orphans []OrphanNode
	Next    string
}

// orphanCandidate is one scanned node of a type that requires a parent.
type orphanCandidate struct {
	node OrphanNode
	kind *node.NodeType
}

// ScanOrphans reads one page of node resolution records of every
// organization after cursor, and returns the nodes without a hierarchy
// parent. One read transaction reads the page and the node types. Each
// candidate then reads its edges in bounded pages, one read transaction per
// page.
func (s *NodeDeleteStore) ScanOrphans(ctx context.Context, cursor string) (page OrphanPage, err error) {
	defer telemetry.FDBOp(ctx, "store.node.orphan_scan")(&err)
	var candidates []orphanCandidate
	err = runNodeReadTransaction(ctx, s.nodes.db, "scan orphan nodes", func(tr fdb.Transaction) error {
		var readErr error
		page, candidates, readErr = readOrphanCandidates(ctx, tr, cursor)
		return readErr
	})
	if err != nil {
		return OrphanPage{Scanned: 0, Orphans: nil, Next: ""}, err
	}
	page.Orphans = []OrphanNode{}
	for _, candidate := range candidates {
		hasParent, parentErr := s.hasHierarchyParent(ctx, candidate)
		if parentErr != nil {
			return OrphanPage{Scanned: 0, Orphans: nil, Next: ""}, parentErr
		}
		if !hasParent {
			page.Orphans = append(page.Orphans, candidate.node)
		}
	}
	return page, nil
}

// readOrphanCandidates reads up to orphanScanPageSize resolution records
// after cursor and returns the nodes of a type that lists CanLiveUnder types.
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
	page := OrphanPage{Scanned: min(len(items), orphanScanPageSize), Orphans: nil, Next: ""}
	if len(items) > orphanScanPageSize {
		items = items[:orphanScanPageSize]
		page.Next = base64.RawURLEncoding.EncodeToString(items[len(items)-1].Key)
	}
	kindsByOrg := make(map[uuid.UUID]map[string]*node.NodeType)
	candidates := make([]orphanCandidate, 0, len(items))
	for _, item := range items {
		nodeID, err := lastTupleID(ctx, item.Key)
		if err != nil {
			return OrphanPage{}, nil, err
		}
		var resolved node.NodeResolve
		if err := json.Unmarshal(item.Value, &resolved); err != nil {
			return OrphanPage{}, nil, nodeOperationFailure(ctx, "decode node resolution "+nodeID.String(), err)
		}
		if kindsByOrg[resolved.OrgID] == nil {
			kindsByOrg[resolved.OrgID] = make(map[string]*node.NodeType)
		}
		kind, err := cachedNodeTypeByKey(ctx, tr, resolved.OrgID, resolved.NodeType, kindsByOrg[resolved.OrgID])
		if err != nil {
			return OrphanPage{}, nil, err
		}
		if kind == nil || len(kind.CanLiveUnder) == 0 {
			continue
		}
		candidates = append(candidates, orphanCandidate{
			node: OrphanNode{ID: nodeID, OrgID: resolved.OrgID, NodeType: resolved.NodeType}, kind: kind,
		})
	}
	return page, candidates, nil
}

// hasHierarchyParent reads the edges from the candidate in pages of
// maxDeleteEdgePage edges, one read transaction per page, and reports
// whether one edge has an existing node of the candidate's organization as
// its target and node.LivesUnder places the candidate under that node.
func (s *NodeDeleteStore) hasHierarchyParent(ctx context.Context, candidate orphanCandidate) (bool, error) {
	keyRange, err := fdb.PrefixRange(relationshipPrefixBySource(candidate.node.OrgID, candidate.node.ID, ""))
	if err != nil {
		return false, nodeOperationFailure(ctx, "create relationship range for node "+candidate.node.ID.String(), err)
	}
	begin := fdb.FirstGreaterOrEqual(keyRange.Begin)
	for {
		found, more := false, false
		var lastKey fdb.Key
		err := runNodeReadTransaction(ctx, s.nodes.db, "read parent edges of node "+candidate.node.ID.String(), func(tr fdb.Transaction) error {
			selection := fdb.SelectorRange{Begin: begin, End: fdb.FirstGreaterOrEqual(keyRange.End)}
			items, readErr := tr.GetRange(selection, fdb.RangeOptions{Limit: maxDeleteEdgePage}).GetSliceWithError()
			if readErr != nil {
				return searchReadFailure(ctx, "read edges of node "+candidate.node.ID.String(), readErr)
			}
			more = len(items) == maxDeleteEdgePage
			if len(items) > 0 {
				lastKey = items[len(items)-1].Key
			}
			var pageErr error
			found, pageErr = pageHasParent(ctx, tr, candidate, items)
			return pageErr
		})
		if err != nil || found || !more {
			return found, err
		}
		begin = fdb.FirstGreaterThan(lastKey)
	}
}

// pageHasParent reports whether one forward edge in items has a target that
// node.LivesUnder places above the candidate.
func pageHasParent(ctx context.Context, tr fdb.Transaction, candidate orphanCandidate, items []fdb.KeyValue) (bool, error) {
	targetIDs := make([]uuid.UUID, 0, len(items))
	for _, item := range items {
		targetID, err := edgeEnd(ctx, item.Key)
		if err != nil {
			return false, err
		}
		if targetID != candidate.node.ID {
			targetIDs = append(targetIDs, targetID)
		}
	}
	targets, err := readHierarchyNodes(ctx, tr, candidate.node.OrgID, targetIDs, make(map[string]*node.NodeType))
	if err != nil {
		return false, err
	}
	for _, target := range targets {
		if target.Found && node.LivesUnder(candidate.kind, target.Kind) {
			return true, nil
		}
	}
	return false, nil
}
