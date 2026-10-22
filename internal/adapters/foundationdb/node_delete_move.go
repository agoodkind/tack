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

// parentIDProperty is the property that stores the ID of a node's hierarchy
// parent.
const parentIDProperty = "parent_id"

// readRootParent reads the child_of edges of rootID inside tr and returns the
// hierarchy parent that node.HierarchyParent derives from those edges.
// readRootParent returns [uuid.Nil] when rootID does not have a child_of edge,
// when rootID has more child_of edges than one page reads, or when the parent
// is not a node of orgID. readRootParent returns an error that matches
// node.ErrNoHierarchyParent when the edges target more than one distinct
// node. The subtree delete does not start when readRootParent returns that
// error.
func readRootParent(ctx context.Context, tr fdb.Transaction, orgID, rootID uuid.UUID) (uuid.UUID, error) {
	edges, err := readDeleteEdges(ctx, tr, orgID, rootID, node.RelChildOf, false)
	if err != nil || len(edges) == 0 || len(edges) > maxDeleteEdgePage {
		return uuid.Nil, err
	}
	targets := make([]uuid.UUID, 0, len(edges))
	for _, edge := range edges {
		targets = append(targets, edge.TargetID)
	}
	parentID, parentErr := node.HierarchyParent(rootID, nil, targets)
	if parentErr != nil {
		return uuid.Nil, nodeOperationFailure(ctx, "read the hierarchy parent of deleted node "+rootID.String(), parentErr)
	}
	parents, err := readHierarchyNodes(ctx, tr, orgID, []uuid.UUID{parentID}, make(map[string]*node.NodeType))
	if err != nil || !parents[0].Found {
		return uuid.Nil, err
	}
	return parentID, nil
}

// moveChild moves child, a direct child of the deleted root, to the root's
// parent when node.LivesUnder places the child's type under the parent's
// type. The move replaces every child_of edge of the child with one child_of
// edge to the parent, sets the child's parent_id to the parent, moves the
// parent_id index entry, schedules the search work of a parent change, and
// records the old and new values of the node and each changed child_of edge
// in changes. It returns true when it moves the child.
func (s *NodeDeleteStore) moveChild(
	ctx context.Context, tr fdb.Transaction, job *node.SubtreeDeleteJob, child hierarchyNode,
	kinds map[string]*node.NodeType, changes *mutationAudit,
) (bool, error) {
	if job.RootParentID == uuid.Nil {
		return false, nil
	}
	parents, err := readHierarchyNodes(ctx, tr, job.OrgID, []uuid.UUID{job.RootParentID}, kinds)
	if err != nil || !parents[0].Found || !node.LivesUnder(child.Kind, parents[0].Kind) {
		return false, err
	}
	edges, err := readDeleteEdges(ctx, tr, job.OrgID, child.ID, node.RelChildOf, false)
	if err != nil || len(edges) > maxDeleteEdgePage {
		return false, err
	}
	current, view, source, err := readNodeAndView(ctx, tr, job.OrgID, child)
	if err != nil || current == nil {
		return false, err
	}
	if _, err := s.nodes.fenceAttempt(ctx, tr, job.OrgID, child.ID); err != nil {
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
	encoded, err := writeNodeRecords(ctx, tr, current, view)
	if err != nil {
		return false, err
	}
	if err := changes.record(nodeTarget(current), source, encoded); err != nil {
		return false, nodeOperationFailure(ctx, "record the move of node "+child.ID.String(), err)
	}
	if err := moveParentIndexEntry(ctx, tr, current, oldProps[parentIDProperty]); err != nil {
		return false, err
	}
	edgeChanges := parentEdgeChanges(job, child.ID, edges, now)
	if err := applyRelationshipChanges(ctx, tr, edgeChanges, changes); err != nil {
		return false, err
	}
	if s.nodes.searchWork {
		if _, err := scheduleSearchChange(ctx, tr, now, job.OrgID, child.ID, searchChangeContent); err != nil {
			return false, err
		}
		if err := scheduleRelatedSearchWork(ctx, tr, now, edgeChanges); err != nil {
			return false, err
		}
	}
	return true, nil
}

// parentEdgeChanges removes every child_of edge in edges and adds one
// child_of edge from childID to the job's root parent.
func parentEdgeChanges(job *node.SubtreeDeleteJob, childID uuid.UUID, edges []deleteEdge, now time.Time) []node.RelationshipChanges {
	removed := make([]*node.Relationship, 0, len(edges))
	for _, edge := range edges {
		removed = append(removed, &node.Relationship{
			OrgID: job.OrgID, SourceID: edge.SourceID, RelationType: edge.RelationType, TargetID: edge.TargetID,
			CreatedBy: uuid.Nil, CreatedAt: time.Time{}, Props: map[string]json.RawMessage{},
		})
	}
	added := &node.Relationship{
		OrgID: job.OrgID, SourceID: childID, RelationType: node.RelChildOf, TargetID: job.RootParentID,
		CreatedBy: job.ActorID, CreatedAt: now, Props: nil,
	}
	return []node.RelationshipChanges{{Add: []*node.Relationship{added}, Remove: removed}}
}

// readNodeAndView reads and decodes the node and view records of target.
// It also returns the encoded node record. If the node record does not exist,
// it returns nil for both records and the bytes. If only the view record does
// not exist, it returns nil for the view.
func readNodeAndView(ctx context.Context, tr fdb.Transaction, orgID uuid.UUID, target hierarchyNode) (*node.Node, *node.NodeView, []byte, error) {
	nodeFuture := tr.Get(fdb.Key(nodeInstanceKey(orgID, target.TypeKey, target.ID)))
	viewFuture := tr.Get(fdb.Key(nodeViewKey(orgID, target.TypeKey, target.ID)))
	encodedNode, err := nodeFuture.Get()
	if err != nil {
		return nil, nil, nil, searchReadFailure(ctx, "read node "+target.ID.String(), err)
	}
	encodedView, err := viewFuture.Get()
	if err != nil {
		return nil, nil, nil, searchReadFailure(ctx, "read node view "+target.ID.String(), err)
	}
	if len(encodedNode) == 0 {
		return nil, nil, nil, nil
	}
	var current node.Node
	if err := json.Unmarshal(encodedNode, &current); err != nil {
		return nil, nil, nil, nodeOperationFailure(ctx, "decode node "+target.ID.String(), err)
	}
	if len(encodedView) == 0 {
		return &current, nil, encodedNode, nil
	}
	var view node.NodeView
	if err := json.Unmarshal(encodedView, &view); err != nil {
		return nil, nil, nil, nodeOperationFailure(ctx, "decode node view "+target.ID.String(), err)
	}
	return &current, &view, encodedNode, nil
}

// moveParentIndexEntry replaces the parent_id index entry of current when the
// old value has one. A type without an indexed parent_id has no entry.
func moveParentIndexEntry(ctx context.Context, tr fdb.Transaction, current *node.Node, oldValue json.RawMessage) error {
	if len(oldValue) == 0 {
		return nil
	}
	oldKey := fdb.Key(nodeByPropertyKey(current.OrgID, current.NodeType, parentIDProperty, encodePropertyValue(oldValue), current.ID))
	existing, err := tr.Get(oldKey).Get()
	if err != nil {
		return searchReadFailure(ctx, "read parent_id index entry of node "+current.ID.String(), err)
	}
	if existing == nil {
		return nil
	}
	tr.Clear(oldKey)
	newValue := current.Props[parentIDProperty]
	tr.Set(fdb.Key(nodeByPropertyKey(current.OrgID, current.NodeType, parentIDProperty, encodePropertyValue(newValue), current.ID)), []byte{})
	return nil
}

// setProperty returns props with name set to value. A nil map becomes a new
// map.
func setProperty(props map[string]json.RawMessage, name string, value json.RawMessage) map[string]json.RawMessage {
	if props == nil {
		props = make(map[string]json.RawMessage, 1)
	}
	props[name] = value
	return props
}
