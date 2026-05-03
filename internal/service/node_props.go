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
	return validateNodeProps(nt, defs, props)
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
	return validateNodeProps(nt, defs, props)
}

func validateNodeProps(
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
	return fmt.Errorf(
		"node type %q props contain undeclared keys %s: %w",
		nt.TypeKey,
		strings.Join(unknown, ", "),
		domain.ErrInvalidArgument,
	)
}

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
