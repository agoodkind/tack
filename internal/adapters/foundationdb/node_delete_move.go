package foundationdb

import (
	"context"
	"encoding/json"
	"maps"
	"strconv"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// moveChild moves child, a direct child of the deleted root, to the root's
// parent when node.LivesUnder places the child's type under the parent's
// type. It calls the write fence for the child. The move replaces every
// child_of edge of the child with one child_of edge to the parent, sets the
// child's parent_id to the parent, moves the parent_id index entry,
// schedules
// the search work of a parent change, and records the old and new values of
// the node and each changed child_of edge in changes.
//
// After replacing the child_of edges, moveChild calls checkStrictRecord on
// the moved child inside the same transaction. If strict validation is
// active
// and the moved record is invalid, moveChild returns the refusal error, and
// the step does not commit. It returns true when it moves the child.
func (s *NodeDeleteStore) moveChild(
	ctx context.Context,
	transaction fdb.Transaction,
	job *node.SubtreeDeleteJob,
	child hierarchyNode,
	kinds map[string]*node.NodeType,
	changes *mutationAudit,
) (bool, error) {
	if job.RootParentID == uuid.Nil {
		return false, nil
	}

	parents, err := readHierarchyNodes(
		ctx,
		transaction,
		job.OrgID,
		[]uuid.UUID{job.RootParentID},
		kinds,
	)
	if err != nil || !parents[0].Found || !node.LivesUnder(child.Kind, parents[0].Kind) {
		return false, err
	}

	edges, err := readDeleteEdges(ctx, transaction, job.OrgID, child.ID, node.RelChildOf, false)
	if err != nil || len(edges) > maxDeleteEdgePage {
		return false, err
	}

	current, view, source, err := readNodeAndView(ctx, transaction, job.OrgID, child)
	if err != nil || current == nil {
		return false, err
	}

	strict, err := s.nodes.fenceAttempt(ctx, transaction, job.OrgID, child.ID)
	if err != nil {
		return false, err
	}

	now := s.nodes.clock.Now().UTC()
	oldProps := maps.Clone(current.Props)
	parentValue := json.RawMessage(strconv.Quote(job.RootParentID.String()))
	current.Props = setProperty(current.Props, parentIDProperty, parentValue)
	current.UpdatedAt, current.UpdatedBy = now, job.ActorID
	if view != nil {
		view.Props = setProperty(view.Props, parentIDProperty, parentValue)
		view.UpdatedAt, view.UpdatedBy = now, job.ActorID
	}

	encoded, err := writeNodeRecords(ctx, transaction, current, view)
	if err != nil {
		return false, err
	}

	if err := changes.record(nodeTarget(current), source, encoded); err != nil {
		return false, nodeOperationFailure(ctx, "record the move of node "+child.ID.String(), err)
	}

	if err := moveParentIndexEntry(
		ctx,
		transaction,
		current,
		oldProps[parentIDProperty],
	); err != nil {
		return false, err
	}

	edgeChanges := parentEdgeChanges(job, child.ID, edges, now)
	if err := applyRelationshipChanges(ctx, transaction, edgeChanges, changes); err != nil {
		return false, err
	}

	if err := checkStrictRecord(ctx, transaction, strict, current); err != nil {
		return false, err
	}

	if s.nodes.searchWork {
		if _, err := scheduleSearchChange(
			ctx,
			transaction,
			now,
			job.OrgID,
			child.ID,
			searchChangeContent,
		); err != nil {
			return false, err
		}

		if err := scheduleRelatedSearchWork(ctx, transaction, now, edgeChanges); err != nil {
			return false, err
		}
	}

	return true, nil
}

// parentEdgeChanges removes every child_of edge in edges and adds one
// child_of edge from childID to the job's root parent.
func parentEdgeChanges(
	job *node.SubtreeDeleteJob,
	childID uuid.UUID,
	edges []deleteEdge,
	now time.Time,
) []node.RelationshipChanges {
	removed := make([]*node.Relationship, 0, len(edges))
	for _, edge := range edges {
		removed = append(
			removed,
			&node.Relationship{
				OrgID:        job.OrgID,
				SourceID:     edge.SourceID,
				RelationType: edge.RelationType,
				TargetID:     edge.TargetID,
				CreatedBy:    uuid.Nil,
				CreatedAt:    time.Time{},
				Props:        map[string]json.RawMessage{},
			},
		)
	}

	added := &node.Relationship{
		OrgID:        job.OrgID,
		SourceID:     childID,
		RelationType: node.RelChildOf,
		TargetID:     job.RootParentID,
		CreatedBy:    job.ActorID,
		CreatedAt:    now,
		Props:        nil,
	}

	return []node.RelationshipChanges{{Add: []*node.Relationship{added}, Remove: removed}}
}
