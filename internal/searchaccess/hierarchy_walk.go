package searchaccess

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// hierarchyWalk records, for one resource, the ancestors read so far, the
// next node to read, and the entry point or hierarchy defect at the end.
type hierarchyWalk struct {
	resource OrgNode
	current  uuid.UUID
	visited  map[uuid.UUID]bool
	entry    uuid.UUID
	defect   error
	done     bool
}

// step advances the walk by one ancestor.
func (w *hierarchyWalk) step(ancestors map[OrgNode]AncestorState) {
	orgID := w.resource.OrgID
	if w.visited[w.current] {
		w.fail(newHierarchyDefect(fmt.Sprintf("hierarchy cycle at %s", w.current)))
		return
	}
	w.visited[w.current] = true
	state := ancestors[OrgNode{OrgID: orgID, NodeID: w.current}]
	switch {
	case !state.Exists || state.OrgID != orgID:
		w.fail(newHierarchyDefect(fmt.Sprintf("ancestor %s is missing or belongs to another organization", w.current)))
		return
	case state.Type == nil:
		w.fail(newHierarchyDefect(fmt.Sprintf("node type %q is missing", state.TypeKey)))
		return
	case state.Type.Features.Has(node.FeatureIsEntryPoint):
		w.finish(w.current)
		return
	}
	parentID, err := hierarchyParent(w.current, state)
	if err != nil {
		w.fail(err)
		return
	}
	if parentID == uuid.Nil {
		w.finish(w.current)
		return
	}
	w.current = parentID
}

func (w *hierarchyWalk) fail(defect error) {
	w.defect = defect
	w.done = true
}

func (w *hierarchyWalk) finish(entryPointID uuid.UUID) {
	w.entry = entryPointID
	w.done = true
}

// hierarchyParent returns the hierarchy parent of nodeID according to
// [node.HierarchyParent], or [uuid.Nil] for a hierarchy root. The function
// returns a hierarchy defect error when [node.HierarchyParent] rejects the
// node's stored child_of edges. The [node.HierarchyParent] function rejects
// edges with more than one distinct target. The function also rejects a node
// without a child_of edge when its type declares CanLiveUnder types.
func hierarchyParent(nodeID uuid.UUID, state AncestorState) (uuid.UUID, error) {
	parentID, err := node.HierarchyParent(nodeID, state.Type, state.ChildOfs)
	if err != nil {
		return uuid.Nil, newHierarchyDefect("resolve hierarchy parent of node "+nodeID.String(), err)
	}
	return parentID, nil
}

func unfinished(walks []*hierarchyWalk) []*hierarchyWalk {
	active := walks[:0:0]
	for _, walk := range walks {
		if !walk.done {
			active = append(active, walk)
		}
	}
	return active
}

// nodeSet lists distinct nodes in insertion order.
type nodeSet struct {
	nodes []OrgNode
	seen  map[OrgNode]bool
}

func newNodeSet() *nodeSet {
	return &nodeSet{nodes: nil, seen: map[OrgNode]bool{}}
}

func (s *nodeSet) add(key OrgNode) {
	if !s.seen[key] {
		s.seen[key] = true
		s.nodes = append(s.nodes, key)
	}
}

// hierarchyDefectError is one hierarchy defect. It matches ErrHierarchyDefect and
// each of its causes.
type hierarchyDefectError struct {
	message string
	causes  []error
}

func newHierarchyDefect(message string, causes ...error) hierarchyDefectError {
	return hierarchyDefectError{message: message, causes: append([]error{ErrHierarchyDefect}, causes...)}
}

func (d hierarchyDefectError) Error() string {
	var text strings.Builder
	text.WriteString(d.message)
	for _, cause := range d.causes {
		text.WriteString(": ")
		text.WriteString(cause.Error())
	}
	return text.String()
}

func (d hierarchyDefectError) Unwrap() []error { return d.causes }
