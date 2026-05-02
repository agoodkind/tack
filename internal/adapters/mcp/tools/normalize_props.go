package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

func normalizeCreateProps(
	ctx context.Context,
	b NodeTypeBinding,
	nt *node.NodeType,
	orgID uuid.UUID,
	scopeID uuid.UUID,
	props map[string]json.RawMessage,
) (map[string]json.RawMessage, uuid.UUID, error) {
	normalized, err := normalizeReferenceProps(ctx, b, nt, orgID, scopeID, props)
	if err != nil {
		return nil, uuid.Nil, err
	}
	parentID := scopeID
	if normalized == nil {
		return nil, parentID, nil
	}
	raw, ok := normalized["parent_id"]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		delete(normalized, "parent_id")
		return normalized, parentID, nil
	}
	parentRef, ok := rawString(raw)
	if !ok || strings.TrimSpace(parentRef) == "" {
		return nil, uuid.Nil, fmt.Errorf("properties.parent_id must be a node reference string: %w", domain.ErrInvalidArgument)
	}
	parentID, err = resolveParentReference(ctx, b, nt, orgID, scopeID, parentRef)
	if err != nil {
		return nil, uuid.Nil, err
	}
	delete(normalized, "parent_id")
	return normalized, parentID, nil
}

func normalizeUpdateProps(
	ctx context.Context,
	b NodeTypeBinding,
	nt *node.NodeType,
	existing *node.NodeView,
	props map[string]json.RawMessage,
) (map[string]json.RawMessage, error) {
	if existing == nil {
		return nil, domain.ErrNotFound
	}
	scopeID := uuidProp(existing, "scope_id")
	if scopeID == uuid.Nil {
		scopeID = uuidProp(existing, "parent_id")
	}
	return normalizeReferenceProps(ctx, b, nt, existing.OrgID, scopeID, props)
}

func normalizeReferenceProps(
	ctx context.Context,
	b NodeTypeBinding,
	nt *node.NodeType,
	orgID uuid.UUID,
	scopeID uuid.UUID,
	props map[string]json.RawMessage,
) (map[string]json.RawMessage, error) {
	if props == nil {
		return nil, nil
	}
	normalized := make(map[string]json.RawMessage, len(props))
	for name, raw := range props {
		normalized[name] = raw
	}
	if b.PropertyDefs == nil {
		return normalized, nil
	}
	defs, err := b.PropertyDefs.List(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list property defs for reference normalization: %w", err)
	}
	for _, def := range defs {
		if !propertyDefAppliesTo(def, nt) || def.ReferenceTargetTypeKey == "" {
			continue
		}
		raw, ok := normalized[def.Name]
		if !ok || len(raw) == 0 || string(raw) == "null" {
			continue
		}
		if def.Type != node.PropertyTypeUUID {
			continue
		}
		targetType := b.Resolver.typeIndex[def.ReferenceTargetTypeKey]
		if targetType == nil {
			return nil, fmt.Errorf("properties.%s target type %q: %w", def.Name, def.ReferenceTargetTypeKey, domain.ErrInvalidArgument)
		}
		ref, ok := rawString(raw)
		if !ok || strings.TrimSpace(ref) == "" {
			return nil, fmt.Errorf("properties.%s must be a node reference string: %w", def.Name, domain.ErrInvalidArgument)
		}
		id, err := resolveReferenceProp(ctx, b.Resolver, orgID, scopeID, targetType, ref)
		if err != nil {
			return nil, fmt.Errorf("properties.%s %q: %w", def.Name, ref, domain.ErrInvalidArgument)
		}
		normalized[def.Name] = mustRawString(id.String())
	}
	return normalized, nil
}

func resolveReferenceProp(
	ctx context.Context,
	resolver *Resolver,
	orgID uuid.UUID,
	scopeID uuid.UUID,
	targetType *node.NodeType,
	ref string,
) (uuid.UUID, error) {
	if targetType.Reference.Strategy == node.ReferenceScopedProperty && !strings.Contains(ref, scopedNodeRefSeparator) && scopeID != uuid.Nil {
		propName := strings.TrimSpace(targetType.Reference.Property)
		if propName == "" {
			return uuid.Nil, domain.ErrInvalidArgument
		}
		return resolver.resolveNodeWithinScope(ctx, orgID, scopeID, targetType.TypeKey, propName, ref)
	}
	id, err := resolver.ResolveTypedNodeID(ctx, targetType, ref)
	if err != nil {
		return uuid.Nil, err
	}
	resolve, err := resolver.reader.Resolve(ctx, id)
	if err != nil {
		return uuid.Nil, err
	}
	if resolve == nil || resolve.OrgID != orgID {
		return uuid.Nil, domain.ErrNotFound
	}
	return id, nil
}

func propertyDefAppliesTo(def *node.PropertyDef, nt *node.NodeType) bool {
	if len(def.AppliesToFeatures) == 0 {
		return true
	}
	return nt.Features.HasAny(def.AppliesToFeatures...)
}

func rawString(raw json.RawMessage) (string, bool) {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

func mustRawString(value string) json.RawMessage {
	raw, _ := json.Marshal(value)
	return raw
}
