package foundationdb

import (
	"context"
	"encoding/json"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// strictChildOfLimit limits the child_of edges that readResultGraph reads to
// two, which identifies a node with more than one parent edge.
const strictChildOfLimit = 2

// readStrictSchema reads record's node type, organization property
// definitions, and graph inside tr. If the parent exists in the same
// organization and its node type definition exists, it adds that definition
// to the schema. It returns a nil schema when record's node type does not
// exist.
func readStrictSchema(ctx context.Context, tr fdb.Transaction, record *node.Node) (*node.Schema, node.ResultGraph, error) {
	graph := node.ResultGraph{OrgID: record.OrgID, ChildOf: nil, Targets: nil}
	kinds := make(map[string]*node.NodeType)
	kind, err := cachedNodeTypeByKey(ctx, tr, record.OrgID, record.NodeType, kinds)
	if err != nil || kind == nil {
		return nil, graph, err
	}
	definitions, err := readSchemaRecords[node.PropertyDef](ctx, tr, propertyDefPrefix(record.OrgID), "property definitions")
	if err != nil {
		return nil, graph, err
	}
	schema := &node.Schema{OrgID: record.OrgID, Types: map[string]*node.NodeType{kind.TypeKey: kind}, Definitions: definitions}
	graph, err = readResultGraph(ctx, tr, definitions, record)
	if err != nil {
		return nil, graph, err
	}
	parent := graph.Targets[parentIDValue(record.Props[parentIDProperty])]
	if !parent.Found || parent.OrgID != record.OrgID {
		return schema, graph, nil
	}
	parentKind, err := cachedNodeTypeByKey(ctx, tr, record.OrgID, parent.NodeType, kinds)
	if err != nil {
		return nil, graph, err
	}
	if parentKind != nil {
		schema.Types[parentKind.TypeKey] = parentKind
	}
	return schema, graph, nil
}

// readResultGraph reads record's child_of targets and the resolution records
// for IDs in its parent_id and reference properties inside tr. These reads
// include writes that tr has already made.
func readResultGraph(ctx context.Context, tr fdb.Transaction, definitions []*node.PropertyDef, record *node.Node) (node.ResultGraph, error) {
	graph := node.ResultGraph{OrgID: record.OrgID, ChildOf: nil, Targets: map[uuid.UUID]node.ResultTarget{}}
	keyRange, err := fdb.PrefixRange(relationshipPrefixBySource(record.OrgID, record.ID, node.RelChildOf))
	if err != nil {
		return graph, nodeOperationFailure(ctx, "create the child_of range of node "+record.ID.String(), err)
	}
	edges, err := tr.GetRange(keyRange, fdb.RangeOptions{Limit: strictChildOfLimit}).GetSliceWithError()
	if err != nil {
		return graph, searchReadFailure(ctx, "read the child_of edges of node "+record.ID.String(), err)
	}
	for _, edge := range edges {
		targetID, err := edgeEnd(ctx, edge.Key)
		if err != nil {
			return graph, err
		}
		graph.ChildOf = append(graph.ChildOf, targetID)
	}
	targetIDs := []uuid.UUID{parentIDValue(record.Props[parentIDProperty])}
	for _, definition := range definitions {
		if definition.Type == node.PropertyTypeUUID && definition.ReferenceTargetTypeKey != "" {
			targetIDs = append(targetIDs, parentIDValue(record.Props[definition.Name]))
		}
	}
	for _, targetID := range targetIDs {
		if targetID == uuid.Nil {
			continue
		}
		target, err := readResultTarget(ctx, tr, targetID)
		if err != nil {
			return graph, err
		}
		graph.Targets[targetID] = target
	}
	return graph, nil
}

// readResultTarget reads the resolution record of targetID inside tr.
func readResultTarget(ctx context.Context, tr fdb.Transaction, targetID uuid.UUID) (node.ResultTarget, error) {
	absent := node.ResultTarget{Found: false, OrgID: uuid.Nil, NodeType: ""}
	encoded, err := tr.Get(fdb.Key(nodeResolveKey(targetID))).Get()
	if err != nil {
		return absent, searchReadFailure(ctx, "read the resolution record of node "+targetID.String(), err)
	}
	if len(encoded) == 0 {
		return absent, nil
	}
	var resolve node.NodeResolve
	if err := json.Unmarshal(encoded, &resolve); err != nil {
		return absent, searchReadFailure(ctx, "decode the resolution record of node "+targetID.String(), err)
	}
	return node.ResultTarget{Found: true, OrgID: resolve.OrgID, NodeType: resolve.NodeType}, nil
}
