package foundationdb

import (
	"context"
	"fmt"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

// checkMutationResult validates result against schema inside tr. For a new
// node, it checks every property. For a stored node, it checks only the
// properties that plan sets or removes. If result is invalid, it returns an
// error that wraps domain.ErrInvalidArgument.
func checkMutationResult(
	ctx context.Context,
	tr fdb.Transaction,
	plan node.MutationPlan,
	source mutationSource,
	result *node.Node,
	schema node.Schema,
) error {
	checked := func(name string) bool {
		_, patched := plan.Props[name]
		return !source.Exists || patched
	}
	graph, err := readResultGraph(ctx, tr, plan, result, schema, checked)
	if err != nil {
		return err
	}

	validated := schema.ValidateResult(plan.NodeType, result.Props, graph)
	if len(validated.Failures) > 0 {
		return mutationRefusal(ctx, plan.NodeID, validated.Err().Error(), node.ErrSchemaRead)
	}

	if err := checkRequestedParent(ctx, tr, plan, result, schema); err != nil {
		return err
	}

	issues := make([]node.PropertyIssue, 0, len(validated.Invalid))
	for _, issue := range validated.Invalid {
		if checked(issue.Property) && ordinaryIssue(issue) {
			issues = append(issues, issue)
		}
	}

	if len(issues) > 0 {
		validated.Invalid = issues
		return mutationRefusal(ctx, plan.NodeID, validated.Err().Error(), domain.ErrInvalidArgument)
	}

	return nil
}

// ordinaryIssue reports whether a write rejects issue before strict
// validation becomes active. checkRequestedParent checks an explicit parent
// request. Strict validation checks the parent of every write. If a managed
// property's stored definition does not apply to that property, the property
// passes until every environment stores the corrected definition.
func ordinaryIssue(issue node.PropertyIssue) bool {
	switch issue.Code {
	case node.PropertyIssueParent:
		return false
	case node.PropertyIssueNotApplicable:
		return !node.IsManagedProperty(issue.Property)
	case node.PropertyIssueUndeclared, node.PropertyIssueValue, node.PropertyIssueOption,
		node.PropertyIssueRequired, node.PropertyIssueReference:
		return true
	}

	return true
}

// checkRequestedParent validates an explicit parent request against the
// current node and type records inside tr.
func checkRequestedParent(
	ctx context.Context,
	tr fdb.Transaction,
	plan node.MutationPlan,
	result *node.Node,
	schema node.Schema,
) error {
	if plan.Parent == nil {
		return nil
	}

	if result.OrgID != plan.OrgID {
		return mutationRefusal(
			ctx,
			plan.NodeID,
			"the child is not a node in the organization",
			domain.ErrInvalidArgument,
		)
	}

	parentID := plan.Parent.TargetID
	parent, err := readMutationSource(ctx, tr, plan.OrgID, parentID)
	if err != nil {
		return err
	}

	if !parent.Exists || parent.Node.OrgID != plan.OrgID {
		return mutationRefusal(
			ctx,
			plan.NodeID,
			"parent "+parentID.String()+" is not a node in the organization",
			domain.ErrInvalidArgument,
		)
	}

	parentType, err := cachedNodeTypeByKey(ctx, tr, plan.OrgID, parent.TypeKey, schema.Types)
	if err != nil {
		return err
	}

	if parentType == nil {
		return mutationRefusal(
			ctx,
			plan.NodeID,
			"the schema does not contain node type "+parent.TypeKey+" of parent "+parentID.String(),
			node.ErrSchemaRead,
		)
	}

	if !node.LivesUnder(schema.Types[plan.NodeType], parentType) {
		return mutationRefusal(
			ctx,
			plan.NodeID,
			fmt.Sprintf(
				"node type %q cannot live under the parent %s of type %q",
				plan.NodeType,
				parentID,
				parentType.TypeKey,
			),
			domain.ErrInvalidArgument,
		)
	}

	return nil
}

// readResultGraph reads the stored node for each checked UUID reference
// value in result inside tr.
func readResultGraph(
	ctx context.Context,
	tr fdb.Transaction,
	plan node.MutationPlan,
	result *node.Node,
	schema node.Schema,
	checked func(string) bool,
) (node.ResultGraph, error) {
	graph := node.ResultGraph{
		OrgID:   plan.OrgID,
		ChildOf: nil,
		Targets: map[uuid.UUID]node.ResultTarget{},
	}

	for _, definition := range schema.Definitions {
		if definition == nil || definition.Type != node.PropertyTypeUUID ||
			definition.ReferenceTargetTypeKey == "" ||
			!checked(definition.Name) {
			continue
		}

		targetID := storedUUID(result.Props[definition.Name])
		if _, read := graph.Targets[targetID]; targetID == uuid.Nil || read {
			continue
		}

		target, err := readMutationSource(ctx, tr, plan.OrgID, targetID)
		if err != nil {
			return graph, err
		}

		graph.Targets[targetID] = node.ResultTarget{
			Found:    target.Exists,
			OrgID:    plan.OrgID,
			NodeType: target.TypeKey,
		}
	}

	return graph, nil
}
