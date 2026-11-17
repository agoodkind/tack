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

// PropertyCommand separates canonical property data from command controls
// that the node must not store directly.
type PropertyCommand struct {
	ParentID uuid.UUID
	Props    map[string]json.RawMessage
}

// ReferenceResolver resolves a user-facing reference string to a canonical
// node UUID.
type ReferenceResolver func(
	ctx context.Context,
	orgID uuid.UUID,
	scopeID uuid.UUID,
	targetType *node.NodeType,
	ref string,
) (uuid.UUID, error)

// CompileCreatePropertyCommand canonicalizes user-facing property input for
// create.
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
	compiled, err := compilePropertyCommand(
		ctx,
		defs,
		typeIndex,
		nt,
		orgID,
		scopeID,
		props,
		resolveReference,
	)
	if err != nil {
		return nil, err
	}

	if compiled == nil {
		return &PropertyCommand{ParentID: scopeID}, nil
	}

	if raw, ok := compiled["parent_id"]; ok && isDeletedPropValue(raw) {
		// An absent or null parent on create means the scope itself.
		delete(compiled, "parent_id")
	}

	parentID, present, err := resolveCommandParent(ctx, nt, orgID, scopeID, compiled, resolveParent)
	if err != nil {
		return nil, err
	}

	if !present {
		parentID = scopeID
	}

	delete(compiled, "parent_id")

	return &PropertyCommand{ParentID: parentID, Props: compiled}, nil
}

// CompileUpdatePropertyCommand canonicalizes property input for update.
// It resolves parent_id like create and stores the canonical UUID in Props
// instead of the printed reference (TACK-523). It sets ParentID only when
// input includes parent_id.
func CompileUpdatePropertyCommand(
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
	compiled, err := compilePropertyCommand(
		ctx,
		defs,
		typeIndex,
		nt,
		orgID,
		scopeID,
		props,
		resolveReference,
	)
	if err != nil {
		return nil, err
	}

	if compiled == nil {
		return &PropertyCommand{ParentID: uuid.Nil, Props: nil}, nil
	}

	parentID, present, err := resolveCommandParent(ctx, nt, orgID, scopeID, compiled, resolveParent)
	if err != nil {
		return nil, err
	}

	if present {
		compiled["parent_id"] = mustRawString(parentID.String())
	}

	return &PropertyCommand{ParentID: parentID, Props: compiled}, nil
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
		if !node.PropertyAppliesTo(def, nt) || def.ReferenceTargetTypeKey == "" ||
			def.Type != node.PropertyTypeUUID {
			continue
		}

		raw, ok := compiled[def.Name]
		if !ok || len(raw) == 0 || string(raw) == "null" {
			continue
		}

		targetType := typeIndex[def.ReferenceTargetTypeKey]
		if targetType == nil {
			return nil, fmt.Errorf(
				"properties.%s target type %q: %w",
				def.Name,
				def.ReferenceTargetTypeKey,
				domain.ErrInvalidArgument,
			)
		}

		ref, ok := rawString(raw)
		if !ok || strings.TrimSpace(ref) == "" {
			return nil, fmt.Errorf(
				"properties.%s must be a node reference string: %w",
				def.Name,
				domain.ErrInvalidArgument,
			)
		}

		id, err := resolveReference(ctx, orgID, scopeID, targetType, ref)
		if err != nil {
			return nil, fmt.Errorf("properties.%s %q: %w", def.Name, ref, domain.ErrInvalidArgument)
		}

		compiled[def.Name] = mustRawString(id.String())
	}

	return compiled, nil
}

func MustRawString(value string) json.RawMessage {
	return mustRawString(value)
}

func mustRawString(value string) json.RawMessage {
	raw, _ := json.Marshal(value)

	return raw
}
