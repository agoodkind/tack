package foundationdb

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

// validateStrictResult returns nil when strict is false. Otherwise, it calls
// schema.Interpret(nodeType, props).Err() on the complete resulting
// properties. For an invalid result, it returns an error from mutationRefusal
// that wraps domain.ErrInvalidArgument. For a schema read failure, it returns
// an error from nodeOperationFailure that wraps node.ErrSchemaRead.
func validateStrictResult(ctx context.Context, nodeID uuid.UUID, schema node.Schema, nodeType string, props map[string]json.RawMessage, strict bool) error {
	if !strict {
		return nil
	}
	resultErr := schema.Interpret(nodeType, props).Err()
	if resultErr == nil {
		return nil
	}
	if errors.Is(resultErr, node.ErrSchemaRead) {
		return nodeOperationFailure(ctx, "validate the complete result of node "+nodeID.String(), resultErr)
	}
	return mutationRefusal(ctx, nodeID, "strict validation refuses the complete result: "+resultErr.Error(), domain.ErrInvalidArgument)
}
