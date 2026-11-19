package foundationdb

import (
	"context"
	"errors"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

func checkStrictRecord(
	ctx context.Context,
	transaction fdb.Transaction,
	strict bool,
	record *node.Node,
) error {
	return checkStrictMutation(ctx, transaction, strict, record, nil)
}

func checkStrictWrite(
	ctx context.Context,
	transaction fdb.Transaction,
	strict bool,
	record *node.Node,
	changesList []node.RelationshipChanges,
) error {
	if err := checkStrictRelationshipAdds(ctx, transaction, strict, changesList); err != nil {
		return err
	}

	return checkStrictRecord(ctx, transaction, strict, record)
}

func checkStrictMutation(
	ctx context.Context,
	transaction fdb.Transaction,
	strict bool,
	result *node.Node,
	parent *node.ParentChange,
) error {
	if !strict {
		return nil
	}

	schema, graph, err := readStrictSchema(ctx, transaction, result)
	if err != nil {
		return err
	}

	if schema == nil {
		return mutationRefusal(
			ctx,
			result.ID,
			"node type "+result.NodeType+" does not exist",
			domain.ErrInvalidArgument,
		)
	}

	if err := applyStrictParent(ctx, transaction, result, parent, schema, &graph); err != nil {
		return err
	}

	return strictResultError(
		ctx,
		result.ID,
		schema.ValidateResult(result.NodeType, result.Props, graph).Err(),
	)
}

func applyStrictParent(
	ctx context.Context,
	transaction fdb.Transaction,
	result *node.Node,
	parent *node.ParentChange,
	schema *node.Schema,
	graph *node.ResultGraph,
) error {
	if parent == nil {
		return nil
	}

	graph.ChildOf = []uuid.UUID{parent.TargetID}
	target, err := readResultTarget(ctx, transaction, parent.TargetID)
	if err != nil {
		return err
	}

	graph.Targets[parent.TargetID] = target
	if !target.Found || target.OrgID != result.OrgID {
		return nil
	}

	kind, err := cachedNodeTypeByKey(ctx, transaction, result.OrgID, target.NodeType, schema.Types)
	if err != nil {
		return err
	}

	if kind != nil {
		schema.Types[kind.TypeKey] = kind
	}

	return nil
}

func strictResultError(ctx context.Context, nodeID uuid.UUID, resultErr error) error {
	if resultErr == nil {
		return nil
	}

	if errors.Is(resultErr, node.ErrSchemaRead) {
		return nodeOperationFailure(
			ctx,
			"validate the complete result of node "+nodeID.String(),
			resultErr,
		)
	}

	return mutationRefusal(
		ctx,
		nodeID,
		"strict validation refuses the complete result: "+resultErr.Error(),
		domain.ErrInvalidArgument,
	)
}
