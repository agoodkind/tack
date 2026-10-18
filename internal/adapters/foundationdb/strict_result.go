package foundationdb

import (
	"context"
	"errors"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

// checkStrictRecord calls checkStrictMutation for record without a parent
// change. It returns nil when strict is false.
func checkStrictRecord(ctx context.Context, tr fdb.Transaction, strict bool, record *node.Node) error {
	return checkStrictMutation(ctx, tr, strict, record, nil)
}

// checkStrictMutation returns nil when strict is false. Otherwise, it reads
// result's schema and graph inside tr and calls schema.ValidateResult on the
// complete properties. When parent is not nil, it sets graph.ChildOf to
// parent.TargetID before validation. If result's node type does not exist,
// it returns an error that wraps domain.ErrInvalidArgument.
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

// strictResultError returns nil for a nil resultErr. For an invalid result,
// it returns an error from mutationRefusal that wraps
// domain.ErrInvalidArgument. For a schema read failure, it returns an error
// from nodeOperationFailure that wraps node.ErrSchemaRead.
func strictResultError(ctx context.Context, nodeID uuid.UUID, resultErr error) error {
	if resultErr == nil {
		return nil
	}
	if errors.Is(resultErr, node.ErrSchemaRead) {
		return nodeOperationFailure(ctx, "validate the complete result of node "+nodeID.String(), resultErr)
	}
	return mutationRefusal(ctx, nodeID, "strict validation refuses the complete result: "+resultErr.Error(), domain.ErrInvalidArgument)
}
