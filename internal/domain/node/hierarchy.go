package node

import "slices"

// LivesUnder reports whether NodeType metadata places child under parent.
// The child's CanLiveUnder list or the parent's CanContain list declares
// the pair. Search access compilation and relationship removal apply this
// rule to find a node's hierarchy parent.
func LivesUnder(child, parent *NodeType) bool {
	if child == nil || parent == nil {
		return false
	}
	return slices.Contains(child.CanLiveUnder, parent.TypeKey) || slices.Contains(parent.CanContain, child.TypeKey)
}
