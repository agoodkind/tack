package foundationdb

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
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

// fenceRecordWrite calls the write fence for record inside tr through
// s.fenceAttempt. When strict validation is active, it reads the node type and
// property definitions for record inside tr and calls validateStrictResult on
// the complete record properties before any write. If the node type does not
// exist, fenceRecordWrite returns an error that wraps domain.ErrInvalidArgument.
func (s *NodeStore) fenceRecordWrite(ctx context.Context, tr fdb.Transaction, record *node.Node) error {
	strict, err := s.fenceAttempt(ctx, tr, record.OrgID, record.ID)
	if err != nil || !strict {
		return err
	}
	kind, err := cachedNodeTypeByKey(ctx, tr, record.OrgID, record.NodeType, make(map[string]*node.NodeType))
	if err != nil {
		return err
	}
	if kind == nil {
		return mutationRefusal(ctx, record.ID, "node type "+record.NodeType+" does not exist", domain.ErrInvalidArgument)
	}
	definitions, err := readSchemaRecords[node.PropertyDef](ctx, tr, propertyDefPrefix(record.OrgID), "property definitions")
	if err != nil {
		return err
	}
	schema := node.Schema{OrgID: record.OrgID, Types: map[string]*node.NodeType{kind.TypeKey: kind}, Definitions: definitions}
	return validateStrictResult(ctx, record.ID, schema, record.NodeType, record.Props, true)
}
