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
// node type, a cycle, a node without exactly one hierarchy parent, or a
// parent_id that differs from the node's child_of edge. A FoundationDB read
// failure is never a hierarchy defect.
var ErrHierarchyDefect = errors.New("search hierarchy is defective")

// OrgNode identifies one node inside its organization.
type OrgNode struct {
	OrgID, NodeID uuid.UUID
}

// AncestorState is the stored view, node type, and child_of targets of one
// node on a hierarchy path. Exists is false when the node has no view. Type
// is nil when no node type uses the view's type key. ParentID is the
// parent_id text of the view, or empty when the view has none.
type AncestorState struct {
	Exists   bool
	OrgID    uuid.UUID
	TypeKey  string
	Type     *node.NodeType
	ChildOfs []uuid.UUID
	ParentID string
}

// HierarchyReader reads the hierarchy state of many nodes. Ancestors returns
// one state for every requested node and an error only for a failed read.
type HierarchyReader interface {
	Ancestors(ctx context.Context, nodes []OrgNode) (map[OrgNode]AncestorState, error)
}

// EntryPoints returns the entry point of every resource: the nearest
// hierarchy ancestor, or the resource itself, with an entry-point type, or
// the hierarchy root when no such node exists. The one stored child_of edge
// of each node defines its hierarchy parent under [node.HierarchyParent],
// and the walk has no depth limit. Each level issues one Ancestors call for
// every walk still in progress, and each node is read at most once. A
// resource with a hierarchy defect receives an error that wraps
// ErrHierarchyDefect in defects instead of an entry point. A failed read
// returns an error for the whole call.
func EntryPoints(ctx context.Context, reader HierarchyReader, resources []OrgNode) (entries map[OrgNode]uuid.UUID, defects map[OrgNode]error, err error) {
	walks := make([]*hierarchyWalk, 0, len(resources))
	for _, resource := range resources {
		walks = append(walks, &hierarchyWalk{
			resource: resource, current: resource.NodeID, visited: map[uuid.UUID]bool{},
			entry: uuid.Nil, defect: nil, done: false,
		})
	}
	ancestors := map[OrgNode]AncestorState{}
	for active := walks; len(active) > 0; active = unfinished(active) {
		if err := readLevel(ctx, reader, active, ancestors); err != nil {
			return nil, nil, err
		}
		for _, walk := range active {
			walk.step(ancestors)
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

// readLevel reads the unread current nodes of active.
func readLevel(ctx context.Context, reader HierarchyReader, active []*hierarchyWalk, ancestors map[OrgNode]AncestorState) error {
	unreadAncestors := newNodeSet()
	for _, walk := range active {
		key := OrgNode{OrgID: walk.resource.OrgID, NodeID: walk.current}
		if _, read := ancestors[key]; !read {
			unreadAncestors.add(key)
		}
	}
	if len(unreadAncestors.nodes) == 0 {
		return nil
	}
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
	return nil
}
