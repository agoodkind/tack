package node

import (
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
)

// ErrReservedParentRelation reports a request to add a child_of edge
// directly. Only child_of edges define a hierarchy parent. The parent_id
// property provides the only way to set a parent. The error matches
// [domain.ErrInvalidArgument].
var ErrReservedParentRelation = fmt.Errorf(
	"the %s relation type is reserved for the parent that parent_id sets: %w",
	RelChildOf,
	domain.ErrInvalidArgument,
)

// ErrNoHierarchyParent reports a node with child_of edges to more than one
// distinct target, or a node without a child_of edge when its type lists
// CanLiveUnder types.
var ErrNoHierarchyParent = errors.New("node requires exactly one hierarchy parent")

// HierarchyParent returns the hierarchy parent of nodeID from its child_of
// edges, using kind as the node type. One distinct target supplies the parent
// regardless of its type.
//
// A node without a child_of edge is a hierarchy root when kind does not list
// CanLiveUnder types. HierarchyParent returns [uuid.Nil] for that root.
//
//   - Every other case returns an error that wraps ErrNoHierarchyParent.
func HierarchyParent(
	nodeID uuid.UUID,
	kind *NodeType,
	childOfTargets []uuid.UUID,
) (uuid.UUID, error) {
	parentID := uuid.Nil

	for _, targetID := range childOfTargets {
		if parentID != uuid.Nil && targetID != parentID {
			return uuid.Nil, hierarchyParentError{
				message: fmt.Sprintf(
					"node %s has child_of edges to %s and %s",
					nodeID,
					parentID,
					targetID,
				),
			}
		}

		parentID = targetID
	}

	if parentID == uuid.Nil && kind != nil && len(kind.CanLiveUnder) > 0 {
		return uuid.Nil, hierarchyParentError{
			message: fmt.Sprintf("node %s of type %q has no child_of edge", nodeID, kind.TypeKey),
		}
	}

	return parentID, nil
}

// hierarchyParentError reports a node that does not satisfy the hierarchy
// parent rule. It matches ErrNoHierarchyParent.
type hierarchyParentError struct {
	message string
}

func (e hierarchyParentError) Error() string { return e.message + ": " + ErrNoHierarchyParent.Error() }

func (e hierarchyParentError) Unwrap() error { return ErrNoHierarchyParent }

// LivesUnder reports whether the child's CanLiveUnder list or the parent's
// CanContain list permits the child and parent type pair.
//
// Subtree deletion uses this rule to decide whether to assign a direct child
// to the deleted node's parent or delete that child with the node.
//
//   - LivesUnder does not determine the hierarchy parent of a stored node.
func LivesUnder(child, parent *NodeType) bool {
	if child == nil || parent == nil {
		return false
	}

	return slices.Contains(child.CanLiveUnder, parent.TypeKey) ||
		slices.Contains(parent.CanContain, child.TypeKey)
}
