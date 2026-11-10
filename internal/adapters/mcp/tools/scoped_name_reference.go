package tools

import (
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// nodeNameReferenceProperty selects the node name in a scoped name reference
// such as "TACK::Sprint 4". The renderer prints this reference for a node of a
// scoped sequence type without a sequence value, and the typed get, update,
// and delete tools resolve it.
const nodeNameReferenceProperty = "name"

// scopedNameIdentifierFor returns "<parent reference>::<name>" for a node
// without a sequence value. The resolver matches the name among the direct
// children of a node at the first level of the scope chain. The function
// returns an empty string when the scope chain of the type has another
// length, when the parent has another type, when the scope of the node is
// not its parent, or when the parent has no reference.
func (rc *renderCtx) scopedNameIdentifierFor(view *node.NodeView, nodeType *node.NodeType) string {
	if view.Name == "" || !rc.livesOneLevelUnderEntryPoint(nodeType) {
		return ""
	}
	parentID := uuidProp(view, "parent_id")
	if parentID == uuid.Nil {
		return ""
	}
	if scopeID := uuidProp(view, "scope_id"); scopeID != uuid.Nil && scopeID != parentID {
		return ""
	}
	parent, err := rc.reader.Get(rc.ctx, parentID)
	if err != nil || parent == nil || parent.NodeType != nodeType.CanLiveUnder[0] {
		return ""
	}
	parentIdent := rc.nodeIdentifier(parentID)
	if parentIdent == "" {
		return ""
	}
	return parentIdent + scopedNodeRefSeparator + view.Name
}

// livesOneLevelUnderEntryPoint reports whether the first parent type of
// nodeType lives directly under the entry point type. The scope chain of such
// a type has one level.
func (rc *renderCtx) livesOneLevelUnderEntryPoint(nodeType *node.NodeType) bool {
	if len(nodeType.CanLiveUnder) == 0 {
		return false
	}
	parentType := rc.typeIndex[nodeType.CanLiveUnder[0]]
	if parentType == nil || len(parentType.CanLiveUnder) == 0 {
		return false
	}
	grandparentType := rc.typeIndex[parentType.CanLiveUnder[0]]
	return grandparentType != nil && grandparentType.Features.Has(node.FeatureIsEntryPoint)
}
