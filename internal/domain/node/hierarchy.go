package node

import (
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
)

// ErrReservedParentRelation reports a request to add a child_of edge
// directly. Only child_of edges define a hierarchy parent, and the parent_id
// property is the one way to set a parent. It matches
// [domain.ErrInvalidArgument].
var ErrReservedParentRelation = fmt.Errorf("the %s relation type is reserved for the parent that parent_id sets: %w",
	RelChildOf, domain.ErrInvalidArgument)

// ErrNoHierarchyParent reports a node with child_of edges to more than one
// distinct target, or a node without a child_of edge when its type lists
// CanLiveUnder types.
var ErrNoHierarchyParent = errors.New("node requires exactly one hierarchy parent")

// HierarchyParent returns the hierarchy parent of nodeID, a node of type
// kind, from the targets of its child_of edges. The one distinct target is
// the parent, whatever its type. A node without a child_of edge is a
// hierarchy root when kind lists no CanLiveUnder type, and HierarchyParent
// returns [uuid.Nil] for it. Every other case returns an error that wraps
// ErrNoHierarchyParent. Search access, the parent removal guard, the
// subtree delete, and the orphan scan all apply this rule.
func HierarchyParent(nodeID uuid.UUID, kind *NodeType, childOfTargets []uuid.UUID) (uuid.UUID, error) {
	parentID := uuid.Nil
	for _, targetID := range childOfTargets {
		if parentID != uuid.Nil && targetID != parentID {
			return uuid.Nil, hierarchyParentError{message: fmt.Sprintf("node %s has child_of edges to %s and %s", nodeID, parentID, targetID)}
		}
		parentID = targetID
	}
	if parentID == uuid.Nil && kind != nil && len(kind.CanLiveUnder) > 0 {
		return uuid.Nil, hierarchyParentError{message: fmt.Sprintf("node %s of type %q has no child_of edge", nodeID, kind.TypeKey)}
	}
	return parentID, nil
}

// hierarchyParentError is one node without exactly one hierarchy parent. It
// matches ErrNoHierarchyParent.
type hierarchyParentError struct {
	message string
}

func (e hierarchyParentError) Error() string { return e.message + ": " + ErrNoHierarchyParent.Error() }

func (e hierarchyParentError) Unwrap() error { return ErrNoHierarchyParent }

// LivesUnder reports whether NodeType metadata places a node of type child
// under a node of type parent. The child's CanLiveUnder list or the parent's
// CanContain list declares the pair. The subtree delete applies it to decide
// whether a direct child of the deleted node moves to the deleted node's
// parent. It never decides the hierarchy parent of a stored node.
func LivesUnder(child, parent *NodeType) bool {
	if child == nil || parent == nil {
		return false
	}
	return slices.Contains(child.CanLiveUnder, parent.TypeKey) || slices.Contains(parent.CanContain, child.TypeKey)
}
