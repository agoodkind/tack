package service

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

func (s *NodeService) validateCreateProps(
	ctx context.Context,
	orgID uuid.UUID,
	nt *node.NodeType,
	props map[string]json.RawMessage,
) error {
	return s.checkDeclaredProps(ctx, orgID, nt, "create", props)
}

// validateUpdateProps refuses caller values for scope_id and sequence and
// checks the declared property names.
func (s *NodeService) validateUpdateProps(
	ctx context.Context,
	orgID uuid.UUID,
	nt *node.NodeType,
	props map[string]json.RawMessage,
	currentProps map[string]json.RawMessage,
) error {
	if err := refuseManagedProps(ctx, nt, "update", props); err != nil {
		return err
	}

	checked := make(map[string]json.RawMessage, len(props))
	for name, raw := range props {
		if _, exists := currentProps[name]; exists && isDeletedPropValue(raw) {
			continue
		}

		checked[name] = raw
	}

	return s.checkDeclaredProps(ctx, orgID, nt, "update", checked)
}

// refuseManagedProps returns an error that wraps domain.ErrInvalidArgument
// when props contains a property that node.IsManagedProperty reports, other
// than the names in allowed. The node service writes those properties itself.
func refuseManagedProps(
	ctx context.Context,
	nt *node.NodeType,
	operation string,
	props map[string]json.RawMessage,
	allowed ...string,
) error {
	names := make([]string, 0, len(props))
	for name := range props {
		if node.IsManagedProperty(name) && !slices.Contains(allowed, name) {
			names = append(names, "properties."+name)
		}
	}

	if len(names) == 0 {
		return nil
	}

	slices.Sort(names)
	refused := fmt.Errorf(
		"the node service sets %s, and a request cannot set them: %w",
		strings.Join(names, ", "),
		domain.ErrInvalidArgument,
	)

	return loggedPropertyCheckError(ctx, nt, operation, refused)
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

	return ValidateNodeProps(ctx, schema, nt, operation, props)
}

// ValidateNodeProps interprets props with schema for nt. It logs and returns
// an error for schema read failures. It logs and refuses property names
// without definitions and names with definitions that do not apply to nt.
// For names with definitions, it exempts scope_id and sequence.
// Refusal errors wrap domain.ErrInvalidArgument and list quoted names in
// sorted order. Errors and log entries include the operation value.
// It ignores value and option issues and returns nil otherwise.
func ValidateNodeProps(
	ctx context.Context,
	schema node.Schema,
	nt *node.NodeType,
	operation string,
	props map[string]json.RawMessage,
) error {
	result := schema.Interpret(nt.TypeKey, props)
	if len(result.Failures) > 0 {
		return loggedPropertyCheckError(ctx, nt, operation, result.Err())
	}

	undeclared := undeclaredPropNames(result)
	if len(undeclared) == 0 {
		return nil
	}

	refused := fmt.Errorf(
		"props contain undeclared keys %s: %w",
		strings.Join(undeclared, ", "),
		domain.ErrInvalidArgument,
	)

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
			if !node.IsManagedProperty(issue.Property) {
				names = append(names, fmt.Sprintf("%q", issue.Property))
			}
		case node.PropertyIssueValue, node.PropertyIssueOption,
			node.PropertyIssueRequired, node.PropertyIssueReference, node.PropertyIssueParent:
		}
	}

	return names
}
