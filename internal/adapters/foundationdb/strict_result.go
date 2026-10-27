package foundationdb

import (
	"context"
	"errors"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

func checkStrictRecord(ctx context.Context, tr fdb.Transaction, strict bool, record *node.Node) error {
	return checkStrictMutation(ctx, tr, strict, record, nil)
}

func checkStrictWrite(ctx context.Context, tr fdb.Transaction, strict bool, record *node.Node, changesList []node.RelationshipChanges) error {
	if err := checkStrictRelationshipAdds(ctx, tr, strict, changesList); err != nil {
		return err
	}
	return checkStrictRecord(ctx, tr, strict, record)
}

func checkStrictMutation(ctx context.Context, tr fdb.Transaction, strict bool, result *node.Node, parent *node.ParentChange) error {
	if !strict {
		return nil
	}
	schema, graph, err := readStrictSchema(ctx, tr, result)
	if err != nil {
		return err
	}
	if schema == nil {
		return mutationRefusal(ctx, result.ID, "node type "+result.NodeType+" does not exist", domain.ErrInvalidArgument)
	}
	if parent != nil {
		graph.ChildOf = []uuid.UUID{parent.TargetID}
	}
	return strictResultError(ctx, result.ID, schema.ValidateResult(result.NodeType, result.Props, graph).Err())
}

func strictResultError(ctx context.Context, nodeID uuid.UUID, resultErr error) error {
	if resultErr == nil {
		return nil
	}
	if errors.Is(resultErr, node.ErrSchemaRead) {
		return nodeOperationFailure(ctx, "validate the complete result of node "+nodeID.String(), resultErr)
	}
	return mutationRefusal(ctx, nodeID, "strict validation refuses the complete result: "+resultErr.Error(), domain.ErrInvalidArgument)
}
