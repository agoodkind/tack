package node

import (
	"fmt"
	"slices"

	"goodkind.io/tack/internal/domain"
)

// ErrReservedParentRelation reports a request to add a child_of edge
// directly. Only child_of edges define a hierarchy parent, and the parent_id
// property is the one way to set a parent. It matches
// [domain.ErrInvalidArgument].
var ErrReservedParentRelation = fmt.Errorf("the %s relation type is reserved for the parent that parent_id sets: %w",
	RelChildOf, domain.ErrInvalidArgument)

// LivesUnder reports whether NodeType metadata places child under parent.
// The child's CanLiveUnder list or the parent's CanContain list declares
// the pair. Search access compilation and relationship removal apply this
// rule to the child_of edges of a node to find its hierarchy parent.
func LivesUnder(child, parent *NodeType) bool {
	if child == nil || parent == nil {
		return false
	}
	return slices.Contains(child.CanLiveUnder, parent.TypeKey) || slices.Contains(parent.CanContain, child.TypeKey)
}
