package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

func (s *NodeService) applyCreatePropertyDefaults(
	ctx context.Context,
	orgID uuid.UUID,
	nt *node.NodeType,
	scopeID uuid.UUID,
	props map[string]json.RawMessage,
) error {
	defs, err := s.propertyDefs.List(ctx, orgID)
	if err != nil {
		return fmt.Errorf("list property defs for %s defaults: %w", nt.TypeKey, err)
	}
	types, err := s.nodeTypes.List(ctx, orgID)
	if err != nil {
		return fmt.Errorf("list node types for %s defaults: %w", nt.TypeKey, err)
	}
	typeIndex := node.BuildTypeIndex(types)
	for _, def := range defs {
		if !appliesTo(def, nt) {
			continue
		}
		if propertyIsPresent(props, def.Name) {
			continue
		}
		if len(def.DefaultValue) > 0 {
			props[def.Name] = append(json.RawMessage(nil), def.DefaultValue...)
			continue
		}
		if def.DefaultReference == nil {
			continue
		}
		targetID, err := s.resolveCreateDefaultReference(ctx, orgID, scopeID, def, typeIndex)
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(targetID.String())
		props[def.Name] = raw
	}
	for _, def := range defs {
		if !appliesTo(def, nt) || !def.Required {
			continue
		}
		if propertyHasValue(props, def.Name) {
			continue
		}
		return fmt.Errorf("missing required property %q for node type %q: %w", def.Name, nt.TypeKey, domain.ErrInvalidArgument)
	}
	return nil
}

func (s *NodeService) resolveCreateDefaultReference(
	ctx context.Context,
	orgID uuid.UUID,
	scopeID uuid.UUID,
	def *node.PropertyDef,
	typeIndex map[string]*node.NodeType,
) (uuid.UUID, error) {
	defaultReference := def.DefaultReference
	if defaultReference.Scope != node.DefaultReferenceScopeCreateScope {
		return uuid.Nil, fmt.Errorf("property %q default reference scope %q: %w", def.Name, defaultReference.Scope, domain.ErrInvalidArgument)
	}
	if scopeID == uuid.Nil {
		return uuid.Nil, fmt.Errorf("property %q default reference requires create scope: %w", def.Name, domain.ErrInvalidArgument)
	}
	targetType := typeIndex[def.ReferenceTargetTypeKey]
	if targetType == nil {
		return uuid.Nil, fmt.Errorf("property %q target type %q: %w", def.Name, def.ReferenceTargetTypeKey, domain.ErrInvalidArgument)
	}
	if targetType.Reference.Strategy != node.ReferenceScopedProperty {
		return uuid.Nil, fmt.Errorf("property %q target type %q reference strategy %q: %w", def.Name, targetType.TypeKey, targetType.Reference.Strategy, domain.ErrInvalidArgument)
	}
	propName := strings.TrimSpace(targetType.Reference.Property)
	if propName == "" {
		return uuid.Nil, fmt.Errorf("property %q target type %q reference property is empty: %w", def.Name, targetType.TypeKey, domain.ErrInvalidArgument)
	}
	return s.resolveDefaultScopedPropertyReference(ctx, orgID, scopeID, targetType, propName, defaultReference.Reference)
}

func (s *NodeService) resolveDefaultScopedPropertyReference(
	ctx context.Context,
	orgID uuid.UUID,
	scopeID uuid.UUID,
	targetType *node.NodeType,
	propName string,
	ref string,
) (uuid.UUID, error) {
	scopeIDRaw, _ := json.Marshal(scopeID.String())
	views, err := s.reader.List(ctx, node.NodeListQuery{
		OrgID:    orgID,
		NodeType: targetType.TypeKey,
		ByProperty: &node.PropertyMatch{
			PropName: "parent_id",
			Value:    scopeIDRaw,
		},
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("list %s children for default reference %q: %w", targetType.TypeKey, ref, err)
	}
	matches := make(map[uuid.UUID]struct{})
	for _, view := range views {
		if matchesDefaultReferenceProperty(view, propName, ref) {
			matches[view.ID] = struct{}{}
		}
	}
	return uniqueDefaultReferenceMatch(matches, targetType.TypeKey, ref)
}

func matchesDefaultReferenceProperty(view *node.NodeView, propName, input string) bool {
	if view == nil {
		return false
	}
	if propName == "name" && strings.EqualFold(view.Name, input) {
		return true
	}
	value := stringPropertyValue(view.Props, propName)
	return value != "" && strings.EqualFold(value, input)
}

func uniqueDefaultReferenceMatch(matches map[uuid.UUID]struct{}, kind, input string) (uuid.UUID, error) {
	switch len(matches) {
	case 0:
		return uuid.Nil, fmt.Errorf("%s default reference %q: %w", kind, input, domain.ErrNotFound)
	case 1:
		for id := range matches {
			return id, nil
		}
	}
	return uuid.Nil, fmt.Errorf("%s default reference %q matched multiple nodes: %w", kind, input, domain.ErrInvalidArgument)
}

func propertyIsPresent(props map[string]json.RawMessage, name string) bool {
	_, ok := props[name]
	return ok
}

func propertyHasValue(props map[string]json.RawMessage, name string) bool {
	raw, ok := props[name]
	if !ok || len(raw) == 0 {
		return false
	}
	return string(raw) != "null"
}

func stringPropertyValue(props map[string]json.RawMessage, name string) string {
	raw, ok := props[name]
	if !ok || len(raw) == 0 {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}
