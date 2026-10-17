package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

var serviceManagedPropNames = map[string]struct{}{
	"parent_id": {},
	"scope_id":  {},
	"sequence":  {},
}

func (s *NodeService) validateCreateProps(
	ctx context.Context,
	orgID uuid.UUID,
	nt *node.NodeType,
	props map[string]json.RawMessage,
) error {
	defs, err := s.propertyDefs.List(ctx, orgID)
	if err != nil {
		return fmt.Errorf("list property defs for %s create validation: %w", nt.TypeKey, err)
	}
	return ValidateNodeProps(nt, defs, props)
}

func (s *NodeService) validateUpdateProps(
	ctx context.Context,
	orgID uuid.UUID,
	nt *node.NodeType,
	props map[string]json.RawMessage,
) error {
	defs, err := s.propertyDefs.List(ctx, orgID)
	if err != nil {
		return fmt.Errorf("list property defs for %s update validation: %w", nt.TypeKey, err)
	}
	return ValidateNodeProps(nt, defs, setUpdateProps(props))
}

func setUpdateProps(props map[string]json.RawMessage) map[string]json.RawMessage {
	setProps := make(map[string]json.RawMessage, len(props))
	for name, raw := range props {
		if isDeletedPropValue(raw) {
			continue
		}
		setProps[name] = raw
	}
	return setProps
}

func isDeletedPropValue(raw json.RawMessage) bool {
	return len(raw) == 0 || string(raw) == "null"
}

// ValidateNodeProps checks each property name in the property map against
// the property definitions for the node type. Each name must match a property
// definition that applies to the node type, except for:
//
//   - parent_id
//   - scope_id
//   - sequence
//
// These names require matching property definitions, but the definitions do
// not have to apply to the node type.
//
// ValidateNodeProps returns nil when every property name passes. Otherwise,
// ValidateNodeProps returns an undeclaredPropsError that lists every property
// name without a matching definition in sorted order.
func ValidateNodeProps(
	nt *node.NodeType,
	defs []*node.PropertyDef,
	props map[string]json.RawMessage,
) error {
	if len(props) == 0 {
		return nil
	}
	allowed := allowedPropNames(nt, defs)
	unknown := unknownPropNames(props, allowed)
	if len(unknown) == 0 {
		return nil
	}
	return undeclaredPropsError{typeKey: nt.TypeKey, keys: unknown}
}

// undeclaredPropsError stores the node type key and the sorted, quoted names
// of every undeclared property.
type undeclaredPropsError struct {
	typeKey string
	keys    []string
}

func (e undeclaredPropsError) Error() string {
	return fmt.Sprintf("node type %q props contain undeclared keys %s: %s",
		e.typeKey, strings.Join(e.keys, ", "), domain.ErrInvalidArgument)
}

func (e undeclaredPropsError) Unwrap() error { return domain.ErrInvalidArgument }

func allowedPropNames(nt *node.NodeType, defs []*node.PropertyDef) map[string]struct{} {
	allowed := make(map[string]struct{}, len(defs))
	for _, def := range defs {
		if appliesTo(def, nt) || isServiceManagedProp(def.Name) {
			allowed[def.Name] = struct{}{}
		}
	}
	return allowed
}

func unknownPropNames(props map[string]json.RawMessage, allowed map[string]struct{}) []string {
	unknown := make([]string, 0)
	for name := range props {
		if _, ok := allowed[name]; ok {
			continue
		}
		unknown = append(unknown, fmt.Sprintf("%q", name))
	}
	sort.Strings(unknown)
	return unknown
}

func isServiceManagedProp(name string) bool {
	_, ok := serviceManagedPropNames[name]
	return ok
}
