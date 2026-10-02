package searchaccess

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// hierarchyWalk is the progress of one resource toward its entry point.
type hierarchyWalk struct {
	resource OrgNode
	current  uuid.UUID
	visited  map[uuid.UUID]bool
	entry    uuid.UUID
	defect   error
	done     bool
}

// step advances the walk by one ancestor.
func (w *hierarchyWalk) step(ancestors map[OrgNode]AncestorState, targets map[OrgNode]TargetState) {
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
		w.fail(newHierarchyDefect(fmt.Sprintf("node type %q of ancestor %s is missing", state.TypeKey, w.current)))
		return
	case state.Type.Features.Has(node.FeatureIsEntryPoint):
		w.finish(w.current)
		return
	}
	parentID, err := hierarchyParent(orgID, w.current, state, targets)
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

// hierarchyParent returns the one hierarchy parent of nodeID: the target of
// a child_of edge that node.LivesUnder accepts. A node without a hierarchy
// parent is a hierarchy root when its type lists no CanLiveUnder type, and
// hierarchyParent returns uuid.Nil for it.
func hierarchyParent(orgID, nodeID uuid.UUID, state AncestorState, targets map[OrgNode]TargetState) (uuid.UUID, error) {
	found := uuid.Nil
	for _, targetID := range state.ChildOfs {
		if targetID == found {
			continue
		}
		target := targets[OrgNode{OrgID: orgID, NodeID: targetID}]
		if !target.Exists || target.OrgID != orgID || !node.LivesUnder(state.Type, target.Type) {
			continue
		}
		if found != uuid.Nil {
			return uuid.Nil, newHierarchyDefect("resolve hierarchy parent of node "+nodeID.String(), ErrNoHierarchyParent)
		}
		found = targetID
	}
	if found == uuid.Nil && len(state.Type.CanLiveUnder) > 0 {
		return uuid.Nil, newHierarchyDefect("resolve hierarchy parent of node "+nodeID.String(), ErrNoHierarchyParent)
	}
	return found, nil
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
