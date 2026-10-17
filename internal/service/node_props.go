package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
	return s.checkDeclaredProps(ctx, orgID, nt, "create", props)
}

func (s *NodeService) validateUpdateProps(
	ctx context.Context,
	orgID uuid.UUID,
	nt *node.NodeType,
	props map[string]json.RawMessage,
) error {
	return s.checkDeclaredProps(ctx, orgID, nt, "update", setUpdateProps(props))
}

// checkDeclaredProps interprets props with the organization schema and
// checks property name declarations for nt.
func (s *NodeService) checkDeclaredProps(
	ctx context.Context,
	orgID uuid.UUID,
	nt *node.NodeType,
	operation string,
	props map[string]json.RawMessage,
) error {
	if len(props) == 0 {
		return nil
	}
	schema, err := s.readSchema(ctx, orgID)
	if err != nil {
		return loggedPropertyCheckError(ctx, nt, operation, err)
	}
	result := schema.Interpret(nt.TypeKey, props)
	if len(result.Failures) > 0 {
		return loggedPropertyCheckError(ctx, nt, operation, result.Err())
	}
	undeclared := undeclaredPropNames(result)
	if len(undeclared) == 0 {
		return nil
	}
	refused := fmt.Errorf("props contain undeclared keys %s: %w", strings.Join(undeclared, ", "), domain.ErrInvalidArgument)
	return loggedPropertyCheckError(ctx, nt, operation, refused)
}

// undeclaredPropNames returns quoted property names that interpretation
// reports as undeclared or not applicable. It omits property names that the
// service accepts for every node type.
func undeclaredPropNames(result node.PropertyResult) []string {
	names := make([]string, 0, len(result.Invalid))
	for _, issue := range result.Invalid {
		switch issue.Code {
		case node.PropertyIssueUndeclared:
			names = append(names, fmt.Sprintf("%q", issue.Property))
		case node.PropertyIssueNotApplicable:
			if !isServiceManagedProp(issue.Property) {
				names = append(names, fmt.Sprintf("%q", issue.Property))
			}
		case node.PropertyIssueValue, node.PropertyIssueOption, node.PropertyIssueRequired, node.PropertyIssueReference:
		}
	}
	return names
}

func (s *NodeService) readSchema(ctx context.Context, orgID uuid.UUID) (node.Schema, error) {
	definitions, err := s.propertyDefs.List(ctx, orgID)
	if err != nil {
		wrapped := fmt.Errorf("list property definitions of organization %s: %w", orgID, err)
		slog.ErrorContext(ctx, "node.schema_read_failed", slog.String("org_id", orgID.String()), slog.String("err", wrapped.Error()))
		return node.Schema{}, wrapped
	}
	types, err := s.nodeTypes.List(ctx, orgID)
	if err != nil {
		wrapped := fmt.Errorf("list node types of organization %s: %w", orgID, err)
		slog.ErrorContext(ctx, "node.schema_read_failed", slog.String("org_id", orgID.String()), slog.String("err", wrapped.Error()))
		return node.Schema{}, wrapped
	}
	return node.Schema{OrgID: orgID, Types: node.BuildTypeIndex(types), Definitions: definitions}, nil
}

func loggedPropertyCheckError(ctx context.Context, nt *node.NodeType, operation string, err error) error {
	wrapped := fmt.Errorf("node type %q %s: %w", nt.TypeKey, operation, err)
	if errors.Is(err, domain.ErrInvalidArgument) {
		slog.WarnContext(ctx, "node.props_refused", slog.String("node_type", nt.TypeKey), slog.String("err", wrapped.Error()))
		return wrapped
	}
	slog.ErrorContext(ctx, "node.props_check_failed", slog.String("node_type", nt.TypeKey), slog.String("err", wrapped.Error()))
	return wrapped
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

func isServiceManagedProp(name string) bool {
	_, ok := serviceManagedPropNames[name]
	return ok
}
