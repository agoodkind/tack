package searchaccess

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// ErrHierarchyDefect reports stored hierarchy data from which no entry point
// follows: a missing ancestor, an ancestor in another organization, a missing
// node type, a cycle, or a node without exactly one hierarchy parent. A
// FoundationDB read failure is never a hierarchy defect.
var ErrHierarchyDefect = errors.New("search hierarchy is defective")

// OrgNode identifies one node inside its organization.
type OrgNode struct {
	OrgID, NodeID uuid.UUID
}

// AncestorState is the stored view, node type, and child_of targets of one
// node on a hierarchy path. Exists is false when the node has no view. Type
// is nil when no node type uses the view's type key.
type AncestorState struct {
	Exists   bool
	OrgID    uuid.UUID
	TypeKey  string
	Type     *node.NodeType
	ChildOfs []uuid.UUID
}

// TargetState is the stored organization and node type of one child_of
// target. Exists is false when the target has no resolve record. Type is nil
// when the target belongs to another organization or its type is missing.
type TargetState struct {
	Exists bool
	OrgID  uuid.UUID
	Type   *node.NodeType
}

// HierarchyReader reads the hierarchy state of many nodes. Each method
// returns one state for every requested node and an error only for a failed
// read.
type HierarchyReader interface {
	Ancestors(ctx context.Context, nodes []OrgNode) (map[OrgNode]AncestorState, error)
	Targets(ctx context.Context, nodes []OrgNode) (map[OrgNode]TargetState, error)
}

// EntryPoints returns the entry point of every resource: the nearest
// hierarchy ancestor, or the resource itself, with an entry-point type, or
// the hierarchy root when no such node exists. NodeType metadata defines the
// hierarchy, and the walk has no depth limit. Each level issues one Ancestors
// call and one Targets call for every walk still in progress, and each node
// is read at most once. A resource with a hierarchy defect receives an error
// that wraps ErrHierarchyDefect in defects instead of an entry point. A failed
// read returns an error for the whole call.
func EntryPoints(ctx context.Context, reader HierarchyReader, resources []OrgNode) (entries map[OrgNode]uuid.UUID, defects map[OrgNode]error, err error) {
	walks := make([]*hierarchyWalk, 0, len(resources))
	for _, resource := range resources {
		walks = append(walks, &hierarchyWalk{
			resource: resource, current: resource.NodeID, visited: map[uuid.UUID]bool{},
			entry: uuid.Nil, defect: nil, done: false,
		})
	}
	ancestors := map[OrgNode]AncestorState{}
	targets := map[OrgNode]TargetState{}
	for active := walks; len(active) > 0; active = unfinished(active) {
		if err := readLevel(ctx, reader, active, ancestors, targets); err != nil {
			return nil, nil, err
		}
		for _, walk := range active {
			walk.step(ancestors, targets)
		}
	}
	entries = make(map[OrgNode]uuid.UUID, len(walks))
	defects = map[OrgNode]error{}
	for _, walk := range walks {
		if walk.defect != nil {
			defects[walk.resource] = walk.defect
			continue
		}
		entries[walk.resource] = walk.entry
	}
	return entries, defects, nil
}

// readLevel reads the unread current nodes of active and then the unread
// child_of targets of those nodes.
func readLevel(ctx context.Context, reader HierarchyReader, active []*hierarchyWalk, ancestors map[OrgNode]AncestorState, targets map[OrgNode]TargetState) error {
	unreadAncestors := newNodeSet()
	for _, walk := range active {
		key := OrgNode{OrgID: walk.resource.OrgID, NodeID: walk.current}
		if _, read := ancestors[key]; !read {
			unreadAncestors.add(key)
		}
	}
	if len(unreadAncestors.nodes) > 0 {
		states, err := reader.Ancestors(ctx, unreadAncestors.nodes)
		if err != nil {
			return WithContext("read "+strconv.Itoa(len(unreadAncestors.nodes))+" hierarchy ancestors", err)
		}
		for _, key := range unreadAncestors.nodes {
			state, found := states[key]
			if !found {
				return fmt.Errorf("read hierarchy ancestor %s: the reader returned no state", key.NodeID)
			}
			ancestors[key] = state
		}
	}
	unreadTargets := newNodeSet()
	for _, walk := range active {
		state := ancestors[OrgNode{OrgID: walk.resource.OrgID, NodeID: walk.current}]
		for _, targetID := range state.ChildOfs {
			key := OrgNode{OrgID: walk.resource.OrgID, NodeID: targetID}
			if _, read := targets[key]; !read {
				unreadTargets.add(key)
			}
		}
	}
	if len(unreadTargets.nodes) == 0 {
		return nil
	}
	states, err := reader.Targets(ctx, unreadTargets.nodes)
	if err != nil {
		return WithContext("read "+strconv.Itoa(len(unreadTargets.nodes))+" hierarchy targets", err)
	}
	for _, key := range unreadTargets.nodes {
		state, found := states[key]
		if !found {
			return fmt.Errorf("read hierarchy target %s: the reader returned no state", key.NodeID)
		}
		targets[key] = state
	}
	return nil
}
