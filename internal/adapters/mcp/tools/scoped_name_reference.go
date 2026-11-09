package tools

import (
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// nodeNameReferenceProperty selects the node name in a scoped reference
// such as "TACK::Sprint 4". The renderer uses this format for a scoped
// sequence node without a sequence value. Typed get, update, and delete
// tools resolve the reference.
const nodeNameReferenceProperty = "name"

// scopedNameIdentifierFor builds "<parent reference>::<name>" for rendering
// a node without a sequence value. The resolver matches the name among the
// direct children of the first scope level. The renderer requires a name,
// a parent of the first declared parent type, and an entry point as that
// parent's first declared parent type. It returns an empty string if a read
// fails, the parent lacks a reference, or a nonempty scope differs from the
// parent.
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

// livesOneLevelUnderEntryPoint checks whether the first declared parent
// type has an entry point as its first declared parent. ScopeChainForType
// returns one level for this type.
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
