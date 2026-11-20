package foundationdb

import (
	"context"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// readParentEdges reads the child_of edges from the candidate in pages of
// maxDeleteEdgePage edges, one read transaction per page. It returns the
// target of every child_of edge and reports whether one target is an
// existing node of the candidate's organization other than the candidate.
func (s *NodeDeleteStore) readParentEdges(
	ctx context.Context,
	candidate orphanCandidate,
) ([]uuid.UUID, bool, error) {
	keyRange, err := fdb.PrefixRange(
		relationshipPrefixBySource(candidate.node.OrgID, candidate.node.ID, node.RelChildOf),
	)
	if err != nil {
		return nil, false, nodeOperationFailure(
			ctx,
			"create child_of range for node "+candidate.node.ID.String(),
			err,
		)
	}

	begin := fdb.FirstGreaterOrEqual(keyRange.Begin)
	targets := []uuid.UUID{}
	hasParent := false

	for {
		more := false
		var lastKey fdb.Key
		var pageTargets []uuid.UUID
		pageParent := false
		err := runNodeReadTransaction(
			ctx,
			s.nodes.db,
			"read child_of edges of node "+candidate.node.ID.String(),
			func(tr fdb.Transaction) error {
				selection := fdb.SelectorRange{
					Begin: begin,
					End:   fdb.FirstGreaterOrEqual(keyRange.End),
				}
				items, readErr := tr.GetRange(
					selection,
					fdb.RangeOptions{Limit: maxDeleteEdgePage},
				).GetSliceWithError()
				if readErr != nil {
					return searchReadFailure(
						ctx,
						"read child_of edges of node "+candidate.node.ID.String(),
						readErr,
					)
				}

				more = len(items) == maxDeleteEdgePage
				if len(items) > 0 {
					lastKey = items[len(items)-1].Key
				}

				var pageErr error
				pageTargets, pageParent, pageErr = pageParentTargets(ctx, tr, candidate, items)

				return pageErr
			},
		)
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

// pageParentTargets returns every child_of target in items, including
// additional parent targets. pageParentTargets reports whether any target is
// an existing node other than the candidate. It does not select one parent
// from several valid targets.
func pageParentTargets(
	ctx context.Context,
	tr fdb.Transaction,
	candidate orphanCandidate,
	items []fdb.KeyValue,
) ([]uuid.UUID, bool, error) {
	targetIDs := make([]uuid.UUID, 0, len(items))
	for _, item := range items {
		targetID, err := edgeEnd(ctx, item.Key)
		if err != nil {
			return nil, false, err
		}

		targetIDs = append(targetIDs, targetID)
	}

	targets, err := readHierarchyNodes(
		ctx,
		tr,
		candidate.node.OrgID,
		targetIDs,
		make(map[string]*node.NodeType),
	)
	if err != nil {
		return nil, false, err
	}

	for _, target := range targets {
		if target.Found && target.ID != candidate.node.ID {
			return targetIDs, true, nil
		}
	}

	return targetIDs, false, nil
}
