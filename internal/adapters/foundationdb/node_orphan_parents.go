package foundationdb

import (
	"context"
	"encoding/json"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// readCandidateParentIDs reads the node record of each candidate inside tr
// and sets the candidate's parentID from its parent_id property. It issues
// every read before it waits on any of them. A missing or unparsable
// parent_id leaves [uuid.Nil].
func readCandidateParentIDs(ctx context.Context, tr fdb.Transaction, candidates []orphanCandidate) error {
	futures := make([]fdb.FutureByteSlice, 0, len(candidates))
	for _, candidate := range candidates {
		futures = append(futures, tr.Get(fdb.Key(nodeInstanceKey(candidate.node.OrgID, candidate.node.NodeType, candidate.node.ID))))
	}
	for position, future := range futures {
		candidate := &candidates[position]
		encoded, err := future.Get()
		if err != nil {
			return searchReadFailure(ctx, "read node "+candidate.node.ID.String(), err)
		}
		if len(encoded) == 0 {
			continue
		}
		var current node.Node
		if err := json.Unmarshal(encoded, &current); err != nil {
			return nodeOperationFailure(ctx, "decode node "+candidate.node.ID.String(), err)
		}
		var parentText string
		if err := json.Unmarshal(current.Props[parentIDProperty], &parentText); err != nil {
			continue
		}
		if parentID, err := uuid.Parse(parentText); err == nil {
			candidate.parentID = parentID
		}
	}
	return nil
}

// readParentEdges reads the child_of edges from the candidate in pages of
// maxDeleteEdgePage edges, one read transaction per page. It returns the
// target of every child_of edge and reports whether one target is an
// existing node of the candidate's organization that node.LivesUnder places
// above the candidate.
func (s *NodeDeleteStore) readParentEdges(ctx context.Context, candidate orphanCandidate) ([]uuid.UUID, bool, error) {
	keyRange, err := fdb.PrefixRange(relationshipPrefixBySource(candidate.node.OrgID, candidate.node.ID, node.RelChildOf))
	if err != nil {
		return nil, false, nodeOperationFailure(ctx, "create child_of range for node "+candidate.node.ID.String(), err)
	}
	begin := fdb.FirstGreaterOrEqual(keyRange.Begin)
	targets := []uuid.UUID{}
	hasParent := false
	for {
		more := false
		var lastKey fdb.Key
		var pageTargets []uuid.UUID
		pageParent := false
		err := runNodeReadTransaction(ctx, s.nodes.db, "read child_of edges of node "+candidate.node.ID.String(), func(tr fdb.Transaction) error {
			selection := fdb.SelectorRange{Begin: begin, End: fdb.FirstGreaterOrEqual(keyRange.End)}
			items, readErr := tr.GetRange(selection, fdb.RangeOptions{Limit: maxDeleteEdgePage}).GetSliceWithError()
			if readErr != nil {
				return searchReadFailure(ctx, "read child_of edges of node "+candidate.node.ID.String(), readErr)
			}
			more = len(items) == maxDeleteEdgePage
			if len(items) > 0 {
				lastKey = items[len(items)-1].Key
			}
			var pageErr error
			pageTargets, pageParent, pageErr = pageParentTargets(ctx, tr, candidate, items)
			return pageErr
		})
		if err != nil {
			return nil, false, err
		}
		targets = append(targets, pageTargets...)
		hasParent = hasParent || pageParent
		if !more {
			return targets, hasParent, nil
		}
		begin = fdb.FirstGreaterThan(lastKey)
	}
}

// pageParentTargets returns the target of each child_of edge in items and
// reports whether node.LivesUnder places the candidate under one existing
// target.
func pageParentTargets(ctx context.Context, tr fdb.Transaction, candidate orphanCandidate, items []fdb.KeyValue) ([]uuid.UUID, bool, error) {
	targetIDs := make([]uuid.UUID, 0, len(items))
	for _, item := range items {
		targetID, err := edgeEnd(ctx, item.Key)
		if err != nil {
			return nil, false, err
		}
		targetIDs = append(targetIDs, targetID)
	}
	targets, err := readHierarchyNodes(ctx, tr, candidate.node.OrgID, targetIDs, make(map[string]*node.NodeType))
	if err != nil {
		return nil, false, err
	}
	for _, target := range targets {
		if target.Found && target.ID != candidate.node.ID && node.LivesUnder(candidate.kind, target.Kind) {
			return targetIDs, true, nil
		}
	}
	return targetIDs, false, nil
}

// extraParentEdges returns the child_of edges of the candidate that do not
// target its parent_id. A candidate without a child_of edge to its parent_id
// returns none, and its edges stay: the scan keeps its only parent edges.
func extraParentEdges(candidate orphanCandidate, targets []uuid.UUID) []ParentEdge {
	hasParentEdge := false
	for _, target := range targets {
		if target == candidate.parentID {
			hasParentEdge = true
		}
	}
	if candidate.parentID == uuid.Nil || !hasParentEdge {
		return nil
	}
	extra := make([]ParentEdge, 0, len(targets))
	for _, target := range targets {
		if target != candidate.parentID {
			extra = append(extra, ParentEdge{NodeID: candidate.node.ID, OrgID: candidate.node.OrgID, TargetID: target})
		}
	}
	return extra
}
