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

// PropertyCommand carries canonical property data plus any command-level
// controls that should not persist directly on the node.
type PropertyCommand struct {
	ParentID uuid.UUID
	Props    map[string]json.RawMessage
}

// ReferenceResolver resolves a user-facing reference string to a canonical node UUID.
type ReferenceResolver func(
	ctx context.Context,
	orgID uuid.UUID,
	scopeID uuid.UUID,
	targetType *node.NodeType,
	ref string,
) (uuid.UUID, error)

// ParentResolver resolves a create-time parent reference to a canonical node UUID.
type ParentResolver func(
	ctx context.Context,
	childType *node.NodeType,
	orgID uuid.UUID,
	scopeID uuid.UUID,
	ref string,
) (uuid.UUID, error)

// CompileCreatePropertyCommand canonicalizes user-facing property input for create.
func CompileCreatePropertyCommand(
	ctx context.Context,
	defs []*node.PropertyDef,
	typeIndex map[string]*node.NodeType,
	nt *node.NodeType,
	orgID uuid.UUID,
	scopeID uuid.UUID,
	props map[string]json.RawMessage,
	resolveReference ReferenceResolver,
	resolveParent ParentResolver,
) (*PropertyCommand, error) {
	compiled, err := compilePropertyCommand(ctx, defs, typeIndex, nt, orgID, scopeID, props, resolveReference)
	if err != nil {
		return nil, err
	}
	parentID := scopeID
	if compiled == nil {
		return &PropertyCommand{ParentID: parentID}, nil
	}
	raw, ok := compiled["parent_id"]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		delete(compiled, "parent_id")
		return &PropertyCommand{ParentID: parentID, Props: compiled}, nil
	}
	parentRef, ok := rawString(raw)
	if !ok || strings.TrimSpace(parentRef) == "" {
		return nil, fmt.Errorf("properties.parent_id must be a node reference string: %w", domain.ErrInvalidArgument)
	}
	parentID, err = resolveParent(ctx, nt, orgID, scopeID, parentRef)
	if err != nil {
		return nil, err
	}
	delete(compiled, "parent_id")
	return &PropertyCommand{ParentID: parentID, Props: compiled}, nil
}

// CompileUpdatePropertyCommand canonicalizes user-facing property input for update.
func CompileUpdatePropertyCommand(
	ctx context.Context,
	defs []*node.PropertyDef,
	typeIndex map[string]*node.NodeType,
	nt *node.NodeType,
	orgID uuid.UUID,
	scopeID uuid.UUID,
	props map[string]json.RawMessage,
	resolveReference ReferenceResolver,
) (*PropertyCommand, error) {
	compiled, err := compilePropertyCommand(ctx, defs, typeIndex, nt, orgID, scopeID, props, resolveReference)
	if err != nil {
		return nil, err
	}
	return &PropertyCommand{Props: compiled}, nil
}

func compilePropertyCommand(
	ctx context.Context,
	defs []*node.PropertyDef,
	typeIndex map[string]*node.NodeType,
	nt *node.NodeType,
	orgID uuid.UUID,
	scopeID uuid.UUID,
	props map[string]json.RawMessage,
	resolveReference ReferenceResolver,
) (map[string]json.RawMessage, error) {
	if props == nil {
		return nil, nil
	}
	compiled, err := canonicalizePropertyCommandKeys(nt, defs, props)
	if err != nil {
		return nil, err
	}
	for _, def := range defs {
		if !propertyDefAppliesToCommand(def, nt) || def.ReferenceTargetTypeKey == "" || def.Type != node.PropertyTypeUUID {
			continue
		}
		raw, ok := compiled[def.Name]
		if !ok || len(raw) == 0 || string(raw) == "null" {
			continue
		}
		targetType := typeIndex[def.ReferenceTargetTypeKey]
		if targetType == nil {
			return nil, fmt.Errorf("properties.%s target type %q: %w", def.Name, def.ReferenceTargetTypeKey, domain.ErrInvalidArgument)
		}
		ref, ok := rawString(raw)
		if !ok || strings.TrimSpace(ref) == "" {
			return nil, fmt.Errorf("properties.%s must be a node reference string: %w", def.Name, domain.ErrInvalidArgument)
		}
		id, err := resolveReference(ctx, orgID, scopeID, targetType, ref)
		if err != nil {
			return nil, fmt.Errorf("properties.%s %q: %w", def.Name, ref, domain.ErrInvalidArgument)
		}
		compiled[def.Name] = mustRawString(id.String())
	}
	return compiled, nil
}

func canonicalizePropertyCommandKeys(
	nt *node.NodeType,
	defs []*node.PropertyDef,
	props map[string]json.RawMessage,
) (map[string]json.RawMessage, error) {
	canonicalNames, aliasMap := allowedPropertyCommandKeys(nt, defs)
	compiled := make(map[string]json.RawMessage, len(props))
	seenNames := make(map[string]string, len(props))
	for name, raw := range props {
		canonicalName, err := canonicalPropertyCommandKey(name, canonicalNames, aliasMap)
		if err != nil {
			return nil, err
		}
		if seenName, exists := seenNames[canonicalName]; exists {
			return nil, fmt.Errorf("properties contain both %q and %q: %w", seenName, name, domain.ErrInvalidArgument)
		}
		seenNames[canonicalName] = name
		compiled[canonicalName] = raw
	}
	return compiled, nil
}

func allowedPropertyCommandKeys(
	nt *node.NodeType,
	defs []*node.PropertyDef,
) (map[string]struct{}, map[string]string) {
	canonicalNames := make(map[string]struct{}, len(defs)+1)
	aliasMap := make(map[string]string)
	for _, def := range defs {
		if !propertyDefAppliesToCommand(def, nt) {
			continue
		}
		canonicalNames[def.Name] = struct{}{}
		if def.ReferenceTargetTypeKey == "" || def.Type != node.PropertyTypeUUID {
			continue
		}
		alias := strings.TrimSuffix(def.Name, "_id")
		if alias == def.Name {
			continue
		}
		aliasMap[alias] = def.Name
	}
	canonicalNames["parent_id"] = struct{}{}
	return canonicalNames, aliasMap
}

func canonicalPropertyCommandKey(
	name string,
	canonicalNames map[string]struct{},
	aliasMap map[string]string,
) (string, error) {
	if _, ok := canonicalNames[name]; ok {
		return name, nil
	}
	if canonicalName, ok := aliasMap[name]; ok {
		return canonicalName, nil
	}
	return "", fmt.Errorf("properties.%s is not declared for this node type: %w", name, domain.ErrInvalidArgument)
}

func propertyDefAppliesToCommand(def *node.PropertyDef, nt *node.NodeType) bool {
	if len(def.AppliesToFeatures) == 0 {
		return true
	}
	return nt.Features.HasAny(def.AppliesToFeatures...)
}

func PropertyDefAppliesToCommand(def *node.PropertyDef, nt *node.NodeType) bool {
	return propertyDefAppliesToCommand(def, nt)
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

func MustRawString(value string) json.RawMessage {
	return mustRawString(value)
}
