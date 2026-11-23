package tools

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func (rc *renderCtx) referenceIdentifierFor(view *node.NodeView) string {
	nodeType := rc.typeIndex[view.NodeType]
	if nodeType == nil {
		return ""
	}

	switch nodeType.Reference.Strategy {
	case node.ReferenceUUIDOnly:
		return ""
	case node.ReferenceDirectProperty:
		return referencePropertyValue(view, nodeType.Reference.Property)
	case node.ReferenceScopedSequence:
		sequence := numberProp(view, nodeType.Reference.Property)
		if sequence <= 0 {
			return rc.scopedNameIdentifierFor(view, nodeType)
		}

		scopeIdent := rc.scopeIdentifierFor(view)
		if scopeIdent == "" {
			return fmt.Sprintf("%s-%d", strings.ToUpper(view.NodeType), sequence)
		}

		return fmt.Sprintf("%s-%d", scopeIdent, sequence)
	case node.ReferenceScopedProperty:
		value := referencePropertyValue(view, nodeType.Reference.Property)
		if value == "" {
			return ""
		}

		scopeIdent := rc.scopeIdentifierFor(view)
		if scopeIdent == "" || strings.EqualFold(scopeIdent, value) {
			return value
		}

		return scopeIdent + scopedNodeRefSeparator + value
	default:
		return ""
	}
}

func (rc *renderCtx) scopeIdentifierFor(view *node.NodeView) string {
	if rc == nil || view == nil {
		return ""
	}

	if scopeID := uuidProp(view, "scope_id"); scopeID != uuid.Nil {
		if scopeIdent := rc.nodeIdentifier(scopeID); scopeIdent != "" {
			return scopeIdent
		}
	}

	parentView := rc.parent(view)
	for depth := 0; depth < maxParentDepth && parentView != nil; depth++ {
		if ident := stringProp(parentView, "identifier"); ident != "" {
			return ident
		}

		parentView = rc.parent(parentView)
	}

	return ""
}

func referencePropertyValue(view *node.NodeView, propName string) string {
	if view == nil || propName == "" {
		return ""
	}

	if propName == nameFieldKey {
		return view.Name
	}

	return stringProp(view, propName)
}
